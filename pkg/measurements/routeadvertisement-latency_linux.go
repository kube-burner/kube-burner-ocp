// Copyright 2025 The Kube-burner Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package measurements

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	k8sconnector "github.com/cloud-bulldozer/go-commons/v2/k8s-connector"
	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements/types"
	"github.com/kube-burner/kube-burner/v2/pkg/util/fileutils"
	probing "github.com/prometheus-community/pro-bing"
	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const (
	// Metric names and the RouteAdvertisement condition used for acceptance timing.
	raLatencyMeasurement          = "raLatencyMeasurement"
	raLatencyQuantilesMeasurement = "raLatencyQuantilesMeasurement"
	raAcceptedConditionType       = "Accepted"
)

var (
	// Default values for the job's input variables. This will be overridden with user passed values in the job template.
	//
	// Import probe scaling: Each of the importRoutesCount routes pings ALL CUDNs in the cluster.
	// Total import probes = importRoutesCount × numCUDNs
	// Example: 200 routes × 50 CUDNs = 10,000 total probes
	// This ensures comprehensive BGP route propagation validation across all cluster networks.

	// Workers that verify routes advertised from the cluster to this host.
	exportWorkerCount = 20
	// Workers that generate external routes and verify their import into the cluster.
	importWorkerCount = 10
	// Max concurrent pings per import route (limits FD usage regardless of CUDN count)
	maxConcurrentImportPings = 5
	// Ping timeouts and retry delays, in milliseconds.
	importWaitBeforePingRetryMsec = 100
	importPingerTimeoutMsec       = 10
	exportWaitBeforePingRetryMsec = 100
	exportPingerTimeoutMsec       = 100
	// number of routes to be imported will be a multiple of numDummyIfaces and numAddressOnDummyIface
	numDummyIfaces         = 20
	numAddressOnDummyIface = 10
	importRoutesCount      = numDummyIfaces * numAddressOnDummyIface

	supportedRaLatencyJobTypes = []config.JobType{config.CreationJob, config.PatchJob}
)

// Internal struct used to marshal PodAnnotation to the pod annotation
// pod IP address is derived from podAnnotation when the pod is created on cudn network
type podAnnotation struct {
	IPs      []string   `json:"ip_addresses"`
	MAC      string     `json:"mac_address"`
	Gateways []string   `json:"gateway_ips,omitempty"`
	Routes   []podRoute `json:"routes,omitempty"`

	IP      string `json:"ip_address,omitempty"`
	Gateway string `json:"gateway_ip,omitempty"`

	TunnelID int    `json:"tunnel_id,omitempty"`
	Role     string `json:"role,omitempty"`
}

// Internal struct used to marshal PodRoute to the pod annotation
type podRoute struct {
	Dest    string `json:"dest"`
	NextHop string `json:"nextHop"`
}

// routeImport represents one source route to add on a dummy interface.
type routeImport struct {
	link string
	addr string
}

type cudnImportTarget struct {
	cudn string
	pod  string
}

// importLatencyAggregate tracks import probe results per CUDN.
// Zero tolerance policy: ANY failure (failures > 0) fails the entire test.
// With zero tolerance enforced, if we reach normalizeMetrics(), all probes succeeded
// and maxLatency holds the worst-case latency (initialized to -1, updated on first success).
type importLatencyAggregate struct {
	maxLatency float64 // Maximum latency of successful probes; -1 if no probes ran
	failures   int     // Count of failed probes; test fails if > 0 (zero tolerance)
}

// stopCh controls the RouteAdvertisement informer, independently of the ping workers.
var stopCh = make(chan struct{})

// raMetric represents all latency measurements for a single RouteAdvertisement.
// One metric is created per RA, with fields populated based on ra-connection-check mode:
//   - "none": Only AcceptedLatency is measured
//   - "export": AcceptedLatency, ExportLatency, NetlinkRouteLatency
//   - "import": AcceptedLatency, ImportLatency, ImportRouteFailures
//   - "export,import": All fields populated
//
// All latency values default to -1 if not measured or not applicable.
type raMetric struct {
	Timestamp  time.Time `json:"timestamp"`
	MetricName string    `json:"metricName"`
	UUID       string    `json:"uuid"`
	JobName    string    `json:"jobName,omitempty"`
	// route advertisement name
	Name     string `json:"routeAdvertisementName"`
	Metadata any    `json:"metadata,omitempty"`
	// CUDN advertised by this route advertisement (assumes one RA maps to one CUDN)
	cudn string
	// ms from RA creation to Accepted=True; -1 if never observed
	AcceptedLatency float64 `json:"acceptedLatency"`
	// Maximum successful import ping latency in ms for this CUDN; -1 if no probe succeeded.
	ImportLatency float64 `json:"importLatency"`
	// Number of failed imported-route probes for this CUDN.
	ImportRouteFailures int `json:"importRouteFailures"`
	// Export ping latency in ms for this CUDN; -1 if not observed or disabled.
	ExportLatency float64 `json:"exportLatency"`
	// netlink route detection latency in ms
	NetlinkRouteLatency float64 `json:"netlinkRouteLatency"`
}

// type holds cudn name and its pods
type cudnPods struct {
	cudn string
	pods []string
}

// type holds route detection and cudn pod's ping timestamp
type netlinkRoutes struct {
	// measure timestamp when a route (belonging to cudn's subnet) detected by the kernel
	routeTimestamp time.Time
	// cudn subnet is exported as route. KB detected this route in its host. Now KB pings one corresponding cudn pod and stores the success timestamp.
	// Zero value means ping did not succeed.
	pingTimestamp time.Time
}

type raLatency struct {
	measurements.BaseMeasurement

	// list of cudn and their pods advertised by this route
	cudnSubnet map[string]cudnPods
	// timestamp when cudn is detected on external host and later events of ping tests
	cudnConnTimestamp sync.Map
	// how many routes verified during export scenario, helpful for closing the export threads
	verifiedExportRouteCount uint64
	verifiedExportRoutes     sync.Map
	// how many route-to-CUDN import probes completed
	verifiedImportProbeCount uint64
	importTargets            []cudnImportTarget
	importLatencyMu          sync.Mutex
	importLatencyByCudn      map[string]importLatencyAggregate
	// how many RAs have Accepted=True recorded, used when ra-connection-check=none
	verifiedAcceptedCount uint64
	// netlink channel we open with the kernel for route monitoring
	routeCh chan netlink.RouteUpdate
	// channel to notify closure of export workers and netlink monitoring
	exportDoneCh chan struct{}
	// channel to notify closure of import workers
	importDoneCh chan struct{}
	// channel used by export workers to report verification failures
	scenarioErrCh chan error
	// channel used to send source routes to import workers
	routeImportChan chan routeImport
	wg              sync.WaitGroup
	connector       k8sconnector.K8SConnector
	// controls which ping tests to run: "none", "export", "import", or "export,import"/"import,export"
	raConnectionCheck string
}

func (r *raLatency) isExportEnabled() bool {
	return strings.Contains(r.raConnectionCheck, "export")
}

func (r *raLatency) isImportEnabled() bool {
	return strings.Contains(r.raConnectionCheck, "import")
}

type raLatencyMeasurementFactory struct {
	measurements.BaseMeasurementFactory
}

var raGVR = schema.GroupVersionResource{
	Group:    "k8s.ovn.org",
	Version:  "v1",
	Resource: "routeadvertisements",
}

var cudnGVR = schema.GroupVersionResource{
	Group:    "k8s.ovn.org",
	Version:  "v1",
	Resource: "clusteruserdefinednetworks",
}

func NewRaLatencyMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]any, labelSelector string) (measurements.MeasurementFactory, error) {
	return raLatencyMeasurementFactory{
		measurements.NewBaseMeasurementFactory(configSpec, measurement, metadata, labelSelector),
	}, nil
}

func (plmf raLatencyMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config, embedCfg *fileutils.EmbedConfiguration) measurements.Measurement {
	return &raLatency{
		BaseMeasurement: plmf.NewBaseLatency(jobConfig, clientSet, restConfig, raLatencyMeasurement, raLatencyQuantilesMeasurement, embedCfg),
	}
}

// unlike default pod network, when a pod is created on udn network, pod ip address is retrieved from pod annotations.
// we create list of cudn subnet and pod ip mappings. CUdn subnet is considered as a route exported to outside the cluster. When KB wants to ping test the cudn, it pings cudn's pods.
func (r *raLatency) getPods() error {
	var err error
	listOptions := metav1.ListOptions{LabelSelector: fmt.Sprintf("%s=%s", config.KubeBurnerLabelUUID, r.Uuid)}
	nsList, err := r.ClientSet.CoreV1().Namespaces().List(context.TODO(), listOptions)
	if err != nil {
		log.Errorf("Error listing namespaces: %v", err)
		return err
	}
	for _, ns := range nsList.Items {
		podList, err := r.ClientSet.CoreV1().Pods(ns.Name).List(context.TODO(), listOptions)
		if err != nil {
			log.Errorf("Error listing pods in namespace %s: %v", ns.Name, err)
			return err
		}
		for _, pod := range podList.Items {
			podNetworks := make(map[string]podAnnotation)
			ovnAnnotation, ok := pod.Annotations["k8s.ovn.org/pod-networks"]
			if ok {
				if err := json.Unmarshal([]byte(ovnAnnotation), &podNetworks); err != nil {
					log.Errorf("failed to unmarshal ovn pod annotation  %v", err)
					continue
				}
				for pnet, val := range podNetworks {
					if pnet != "default" {
						var udn string
						parts := strings.Split(pnet, "/")
						if len(parts) == 2 {
							_, udn = parts[0], parts[1]
						} else {
							log.Debugf("Invalid input format")
							continue
						}
						ipAddr, subnet, err := net.ParseCIDR(val.IP)
						if err != nil {
							log.Debugf("Unable to get CIDR for IP")
							continue
						}
						subnetString := subnet.String()
						ipAddrString := ipAddr.String()
						cudnpods, exists := r.cudnSubnet[subnetString]
						if exists {
							cudnpods.pods = append(cudnpods.pods, ipAddrString)
							r.cudnSubnet[subnetString] = cudnpods
						} else {
							r.cudnSubnet[subnetString] = cudnPods{
								cudn: udn,
								pods: []string{ipAddrString},
							}
						}
					}
				}
			}
		}
	}
	return nil
}

// Record RouteAdvertisement name and creation timestamp when routeadvertisement resource is detected by the API.
// Assumes one RA maps to exactly one CUDN.
func (r *raLatency) handleAdd(obj any) {
	ra := obj.(*unstructured.Unstructured)
	networkSelectors, found, err := unstructured.NestedSlice(ra.UnstructuredContent(), "spec", "networkSelectors")
	if err != nil {
		log.Error(err)
		return
	}
	if !found || len(networkSelectors) == 0 {
		log.Errorf("No networkSelectors found")
		return
	}
	networkSelector, ok := networkSelectors[0].(map[string]interface{})
	if !ok {
		log.Errorf("NetworkSelector doesn't exist")
		return
	}
	labels, found, err := unstructured.NestedStringMap(networkSelector, "clusterUserDefinedNetworkSelector", "networkSelector", "matchLabels")
	if err != nil {
		log.Error(err)
		return
	}
	if !found {
		log.Errorf("No labels found in networkSelector")
		return
	}

	ls := &metav1.LabelSelector{}
	err = metav1.Convert_Map_string_To_string_To_v1_LabelSelector(&labels, ls, nil)
	if err != nil {
		log.Error(err)
		return
	}
	selector, err := metav1.LabelSelectorAsSelector(ls)
	if err != nil {
		log.Error(err)
		return
	}
	listOptions := metav1.ListOptions{}
	listOptions.LabelSelector = selector.String()
	udns, err := r.connector.DynamicClient().Resource(cudnGVR).Namespace(metav1.NamespaceAll).List(context.TODO(), listOptions)
	if err != nil {
		log.Error(err)
		return
	}
	if len(udns.Items) == 0 {
		log.Errorf("No CUDN found for RA selector")
		return
	}
	cudn, _, _ := unstructured.NestedString(udns.Items[0].UnstructuredContent(), "metadata", "name")
	raName, _, _ := unstructured.NestedString(ra.UnstructuredContent(), "metadata", "name")
	ts, _, _ := unstructured.NestedString(ra.UnstructuredContent(), "metadata", "creationTimestamp")
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		log.Error(err)
		return
	}
	acceptedLatency := float64(-1)
	if transitionTime, ok := getRAAcceptedTransitionTime(ra); ok {
		acceptedLatency = float64(transitionTime.Sub(t.UTC()).Milliseconds())
		log.Debugf("RA %s already has Accepted=True, acceptedLatency: %vms", raName, acceptedLatency)
	}
	log.Debugf("RA %s discovered at: %v created at: %v cudn: %s", raName, time.Now().UTC(), t.UTC(), cudn)
	_, loaded := r.Metrics.LoadOrStore(raName, raMetric{
		Name:            raName,
		Timestamp:       t.UTC(),
		cudn:            cudn,
		MetricName:      raLatencyMeasurement,
		UUID:            r.Uuid,
		Metadata:        r.Metadata,
		JobName:         r.JobConfig.Name,
		AcceptedLatency: acceptedLatency,
	})
	if !loaded && acceptedLatency >= 0 {
		atomic.AddUint64(&r.verifiedAcceptedCount, 1)
	}
}

// handleUpdate records Accepted latency the first time the RA reaches Accepted=True.
func (r *raLatency) handleUpdate(_, newObj any) {
	ra := newObj.(*unstructured.Unstructured)
	transitionTime, accepted := getRAAcceptedTransitionTime(ra)
	if !accepted {
		return
	}
	raName, _, _ := unstructured.NestedString(ra.UnstructuredContent(), "metadata", "name")
	val, ok := r.Metrics.Load(raName)
	if !ok {
		return
	}
	m := val.(raMetric)
	if m.AcceptedLatency >= 0 {
		return // Already recorded
	}
	m.AcceptedLatency = float64(transitionTime.Sub(m.Timestamp).Milliseconds())
	r.Metrics.Store(raName, m)
	atomic.AddUint64(&r.verifiedAcceptedCount, 1)
	log.Debugf("RA %s Accepted after %vms", raName, m.AcceptedLatency)
}

// getRAAcceptedTransitionTime returns the lastTransitionTime of the Accepted=True
// condition, and true if found.
func getRAAcceptedTransitionTime(ra *unstructured.Unstructured) (time.Time, bool) {
	conditions, found, err := unstructured.NestedSlice(ra.UnstructuredContent(), "status", "conditions")
	if err != nil || !found {
		return time.Time{}, false
	}
	for _, c := range conditions {
		condition, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		condType, _, _ := unstructured.NestedString(condition, "type")
		condStatus, _, _ := unstructured.NestedString(condition, "status")
		if condType == raAcceptedConditionType && condStatus == "True" {
			ltt, _, _ := unstructured.NestedString(condition, "lastTransitionTime")
			if t, err := time.Parse(time.RFC3339, ltt); err == nil {
				return t.UTC(), true
			}
			log.Warnf("RA %s: Accepted=True but missing/invalid lastTransitionTime, using current time", ra.GetName())
			return time.Now().UTC(), true
		}
	}
	return time.Time{}, false
}

// ping the given pod address
func pingAddress(srcIP, destIP string, pingerTimeoutMsec int) error {
	pinger, err := probing.NewPinger(destIP)
	if err != nil {
		log.Debugf("Failed to create pinger for %s: %v", destIP, err)
		return err
	}

	if srcIP != "" {
		// Required for raw sockets (root access needed)
		pinger.SetPrivileged(true)
		// Bind to the specified source IP
		pinger.Source = srcIP
	}
	pinger.Count = 1
	pinger.Timeout = time.Duration(pingerTimeoutMsec) * time.Millisecond
	if err := pinger.Run(); err != nil {
		log.Debugf("Ping to %s failed: %v", destIP, err)
		return err
	}
	if pinger.Statistics().PacketsRecv == 0 {
		return fmt.Errorf("ping to %s timed out without receiving a reply", destIP)
	}
	return nil
}

/*
During the import scenario, ping one CUDN pod to validate that the CUDN's gateway router imported the external route.

Pings indefinitely with a sleep between attempts until success or done is closed
(scenario timeout / stop). Returns latency in ms and nil when the ping succeeds,
or -1 and the last error when aborted.
*/
func pingOneCudn(destPod, srcAddr string, routeAddedAt time.Time, done <-chan struct{}) (float64, error) {
	var pingErr error
	for {
		select {
		case <-done:
			if pingErr == nil {
				pingErr = fmt.Errorf("import ping aborted")
			}
			return -1, pingErr
		default:
		}
		if pingErr = pingAddress(srcAddr, destPod, importPingerTimeoutMsec); pingErr == nil {
			return float64(time.Since(routeAddedAt).Milliseconds()), nil
		}
		select {
		case <-done:
			return -1, pingErr
		case <-time.After(time.Duration(importWaitBeforePingRetryMsec) * time.Millisecond):
		}
	}
}

/*
Each import worker adds one source address, then pings every CUDN target concurrently
using that address as the source. The worker aggregates one result per route/CUDN pair.
*/

func (r *raLatency) importWorker() {
	defer r.wg.Done()
	for {
		select {
		case mc, ok := <-r.routeImportChan:
			if !ok {
				return
			}
			link, err := netlink.LinkByName(mc.link)
			if err != nil {
				log.Errorf("Failed to get link %s: %v", mc.link, err)
				r.recordImportFailures()
				continue
			}
			addr, err := netlink.ParseAddr(mc.addr)
			if err != nil {
				log.Errorf("Failed to parse address %s: %v", mc.addr, err)
				r.recordImportFailures()
				continue
			}
			ipAddr, _, err := net.ParseCIDR(mc.addr)
			if err != nil {
				log.Errorf("Failed to parse CIDR %s: %v", mc.addr, err)
				r.recordImportFailures()
				continue
			}
			if err := netlink.AddrAdd(link, addr); err != nil {
				log.Errorf("Failed to add address %s: %v", mc.addr, err)
				r.recordImportFailures()
				continue
			}

			routeAddedAt := time.Now()
			sourceIP := ipAddr.String()
			// Each import route pings ALL CUDNs to verify BGP route propagation.
			// Total probes = importRoutesCount × numCUDNs (e.g., 200 routes × 50 CUDNs = 10,000 probes)
			// Semaphore limits concurrent pings per route to prevent FD exhaustion.
			sem := make(chan struct{}, maxConcurrentImportPings)
			var probeWg sync.WaitGroup
			for _, target := range r.importTargets {
				probeWg.Add(1)
				go func(target cudnImportTarget) {
					defer probeWg.Done()
					sem <- struct{}{}        // Acquire semaphore slot
					defer func() { <-sem }() // Release semaphore slot
					if target.pod == "" {
						r.recordImportProbe(target.cudn, -1)
						return
					}
					latency, err := pingOneCudn(target.pod, sourceIP, routeAddedAt, r.importDoneCh)
					if err != nil {
						log.Debugf("Import route %s failed for CUDN %s: %v", mc.addr, target.cudn, err)
					}
					r.recordImportProbe(target.cudn, latency)
				}(target)
			}
			probeWg.Wait()

		case <-r.importDoneCh:
			return
		}
	}
}

func (r *raLatency) recordImportProbe(cudn string, latency float64) {
	r.importLatencyMu.Lock()
	aggregate := r.importLatencyByCudn[cudn]
	if latency < 0 {
		aggregate.failures++
	} else if latency > aggregate.maxLatency {
		// Track worst-case (maximum) successful probe latency
		aggregate.maxLatency = latency
	}
	r.importLatencyByCudn[cudn] = aggregate
	r.importLatencyMu.Unlock()
	atomic.AddUint64(&r.verifiedImportProbeCount, 1)
}

func (r *raLatency) recordImportFailures() {
	for _, target := range r.importTargets {
		r.recordImportProbe(target.cudn, -1)
	}
}

/*
When KB creates RA CRDs, CUDN subnets are advertised. BGP's external frr adds these as linux routes.
Now kernel notifies KB about these routes using "routeCh" channel (which KB subscribed to earlier).

This export go thread, reads the route (subnet), retrieves the corresponding cudn (from r.cudnSubnet) and pings one of its pods.
*/

func (r *raLatency) exportWorker() {
	defer r.wg.Done()
	for {
		select {
		case update, ok := <-r.routeCh:
			if !ok {
				return
			}
			if update.Type == unix.RTM_NEWROUTE {
				cudnpods, exists := r.cudnSubnet[update.Dst.String()]
				if exists {
					log.Debugf("Netlink route: %s received for udn: %s at: %v", update.Dst.String(), cudnpods.cudn, time.Now().UTC())
					val, loaded := r.cudnConnTimestamp.LoadOrStore(cudnpods.cudn, netlinkRoutes{
						routeTimestamp: time.Now().UTC(),
					})
					if loaded {
						// Another event already claimed this CUDN; keep serving other CUDNs.
						continue
					}
					if nlRouteVal, ok := val.(netlinkRoutes); ok {
						if nlRouteVal.pingTimestamp.IsZero() {
							if len(cudnpods.pods) == 0 {
								r.scenarioErrCh <- fmt.Errorf("verify export route %s for CUDN %s: no pods available", update.Dst.String(), cudnpods.cudn)
								return
							}
							pod := cudnpods.pods[0]
							for {
								select {
								case <-r.exportDoneCh:
									return
								default:
								}
								if pingErr := pingAddress("", pod, exportPingerTimeoutMsec); pingErr == nil {
									log.Debugf("Ping success to pod %s for the Netlink route: %s received for udn: %s at: %v", pod, update.Dst.String(), cudnpods.cudn, time.Now().UTC())
									nlRouteVal.pingTimestamp = time.Now().UTC()
									break
								}
								select {
								case <-r.exportDoneCh:
									return
								case <-time.After(time.Duration(exportWaitBeforePingRetryMsec) * time.Millisecond):
								}
							}
						}
						if !nlRouteVal.pingTimestamp.IsZero() {
							r.cudnConnTimestamp.Store(cudnpods.cudn, nlRouteVal)
							if _, loaded := r.verifiedExportRoutes.LoadOrStore(update.Dst.String(), struct{}{}); !loaded {
								atomic.AddUint64(&r.verifiedExportRouteCount, 1)
							}
						}
					}
				}
			}
		case <-r.exportDoneCh:
			return
		}
	}
}

// deleteDummyInterface removes an import interface and its assigned addresses/routes.
func (r *raLatency) deleteDummyInterface(i int) error {
	var err error
	ifaceName := fmt.Sprintf("dummy%d", i)
	link, err := netlink.LinkByName(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to find interface %s: %v", ifaceName, err)
	}

	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("failed to delete interface %s: %v", ifaceName, err)
	}
	return err
}

// createDummyInterface creates and brings up dummy<i> for import verification.
// Routes are generated later, when import workers assign addresses to it.
func (r *raLatency) createDummyInterface(i int) error {
	var err error
	// Define the name of the dummy interface
	ifaceName := fmt.Sprintf("dummy%d", i)

	// Create a dummy link (interface)
	dummy := &netlink.Dummy{
		LinkAttrs: netlink.LinkAttrs{
			Name: ifaceName,
		},
	}

	// Add the dummy interface
	if err := netlink.LinkAdd(dummy); err != nil {
		log.Errorf("Failed to add dummy interface: %v", err)
		return err
	}

	// Bring the interface up
	if err := netlink.LinkSetUp(dummy); err != nil {
		log.Errorf("Failed to bring up interface: %v", err)
		return err
	}

	// Verify interface exists
	_, err = netlink.LinkByName(ifaceName)
	if err != nil {
		log.Errorf("Failed to get interface: %v", err)
	}
	return err
}

// startImportScenario creates one deterministic ping target per CUDN, creates
// dummy interfaces, and queues one unique source CIDR per interface/address pair.
func (r *raLatency) startImportScenario() error {
	var err error
	targetPods := make(map[string][]string)
	for _, cpods := range r.cudnSubnet {
		targetPods[cpods.cudn] = append(targetPods[cpods.cudn], cpods.pods...)
	}
	// Include CUDNs with an RA but no discovered pod so their probes are
	// represented as failures instead of silently disappearing.
	r.Metrics.Range(func(_, value any) bool {
		metric := value.(raMetric)
		if _, exists := targetPods[metric.cudn]; !exists {
			targetPods[metric.cudn] = nil
		}
		return true
	})
	cudnNames := make([]string, 0, len(targetPods))
	for cudn := range targetPods {
		cudnNames = append(cudnNames, cudn)
	}
	slices.Sort(cudnNames)
	r.importTargets = make([]cudnImportTarget, 0, len(cudnNames))
	r.importLatencyMu.Lock()
	r.importLatencyByCudn = make(map[string]importLatencyAggregate, len(cudnNames))
	for _, cudn := range cudnNames {
		pods := targetPods[cudn]
		slices.Sort(pods)
		pod := ""
		if len(pods) > 0 {
			pod = pods[0]
		} else {
			log.Warnf("No pods found for CUDN %s; import probes will fail", cudn)
		}
		r.importTargets = append(r.importTargets, cudnImportTarget{cudn: cudn, pod: pod})
		r.importLatencyByCudn[cudn] = importLatencyAggregate{maxLatency: -1}
	}
	r.importLatencyMu.Unlock()
	if len(r.importTargets) == 0 {
		return fmt.Errorf("no CUDN targets found for import route checks")
	}
	for i := range numDummyIfaces {
		err = r.createDummyInterface(i)
		if err != nil {
			return err
		}
	}
	importRoutesCount = numAddressOnDummyIface * numDummyIfaces
	r.routeImportChan = make(chan routeImport, importRoutesCount)
	for i := range numAddressOnDummyIface {
		for j := range numDummyIfaces {
			mm := routeImport{
				link: fmt.Sprintf("dummy%d", j),
				addr: fmt.Sprintf("20.%d.%d.1/24", j, i+1),
			}
			r.routeImportChan <- mm
		}
	}
	close(r.routeImportChan)
	// Start import worker goroutines
	for range importWorkerCount {
		r.wg.Add(1)
		go r.importWorker()
	}
	return nil
}

// startRAInformer registers an informer for RouteAdvertisement resource creation/update events.
// This is always needed to measure RA accepted latency regardless of the ra-connection-check mode.
func (r *raLatency) startRAInformer() error {
	log.Infof("Creating Router Advertisement latency watcher for %s", r.JobConfig.Name)
	connector, err := k8sconnector.NewK8SConnector(r.RestConfig)
	if err != nil {
		log.Error(err)
		return err
	}
	r.connector = connector
	raFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(r.connector.DynamicClient(), time.Minute, metav1.NamespaceAll, nil)
	raInformer := raFactory.ForResource(raGVR).Informer()
	raInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    r.handleAdd,
		UpdateFunc: r.handleUpdate,
	})
	raFactory.Start(stopCh)
	raFactory.WaitForCacheSync(stopCh)
	return nil
}

// startExportScenario subscribes to kernel route updates and starts export workers.
// Stop closes exportDoneCh to stop the subscription and signal export workers,
// then waits for them through r.wg. The subscription itself is not in that group.
func (r *raLatency) startExportScenario() error {
	r.routeCh = make(chan netlink.RouteUpdate, 10000)

	if err := netlink.RouteSubscribe(r.routeCh, r.exportDoneCh); err != nil {
		log.Errorf("Failed to subscribe to route updates: %v", err)
		return err
	}

	// Start export worker goroutines
	for range exportWorkerCount {
		r.wg.Add(1)
		go r.exportWorker()
	}
	return nil
}

// setInputVars applies route counts and enabled checks from job objects.
func (r *raLatency) setInputVars() {
	for _, obj := range r.JobConfig.Objects {
		if val, ok := obj.InputVars["exportWorkerCount"]; ok {
			exportWorkerCount = val.(int)
		}
		if val, ok := obj.InputVars["importWorkerCount"]; ok {
			importWorkerCount = val.(int)
		}
		if val, ok := obj.InputVars["maxConcurrentImportPings"]; ok {
			maxConcurrentImportPings = val.(int)
		}
		if val, ok := obj.InputVars["importWaitBeforePingRetryMsec"]; ok {
			importWaitBeforePingRetryMsec = val.(int)
		}
		if val, ok := obj.InputVars["importPingerTimeoutMsec"]; ok {
			importPingerTimeoutMsec = val.(int)
		}
		if val, ok := obj.InputVars["exportWaitBeforePingRetryMsec"]; ok {
			exportWaitBeforePingRetryMsec = val.(int)
		}
		if val, ok := obj.InputVars["exportPingerTimeoutMsec"]; ok {
			exportPingerTimeoutMsec = val.(int)
		}
		if val, ok := obj.InputVars["numDummyIfaces"]; ok {
			numDummyIfaces = val.(int)
		}
		if val, ok := obj.InputVars["numAddressOnDummyIface"]; ok {
			numAddressOnDummyIface = val.(int)
		}
		if val, ok := obj.InputVars["raConnectionCheck"]; ok {
			r.raConnectionCheck = val.(string)
		}
	}
}

// Start resets measurement state, discovers ping targets, and starts the RA informer.
// Export workers start here if enabled; import workers start later in Stop.
// measurementWg signals initialization completion and is separate from r.wg.
func (r *raLatency) Start(measurementWg *sync.WaitGroup) error {
	// Reset latency slices, required in multi-job benchmarks
	r.LatencyQuantiles, r.NormLatencies = nil, nil
	r.Metrics = sync.Map{}
	atomic.StoreUint64(&r.verifiedAcceptedCount, 0)
	atomic.StoreUint64(&r.verifiedExportRouteCount, 0)
	atomic.StoreUint64(&r.verifiedImportProbeCount, 0)
	r.importTargets = nil
	r.importLatencyByCudn = nil
	r.cudnConnTimestamp = sync.Map{}
	r.verifiedExportRoutes = sync.Map{}

	defer measurementWg.Done()

	if r.JobConfig.SkipIndexing {
		return nil
	}
	r.setInputVars()

	// channels to notify workers to exit (separate channels for independent lifecycle)
	r.exportDoneCh = make(chan struct{})
	r.importDoneCh = make(chan struct{})
	r.scenarioErrCh = make(chan error, exportWorkerCount+importWorkerCount)

	// cudn pods which will be pinged during both import and export scenarios
	r.cudnSubnet = make(map[string]cudnPods)

	// Maintain a list of cudn subnets and their pods, which will be used in both export and import scenarios
	r.getPods()

	// Always start RA informer for accepted latency measurement
	if err := r.startRAInformer(); err != nil {

		return err
	}

	// Only start export ping workers if export is enabled
	if r.isExportEnabled() {
		if err := r.startExportScenario(); err != nil {
			return err
		}
	}
	return nil
}

// Collect only signals completion; informer callbacks and workers gather the data.
func (r *raLatency) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

// waitForSceanrioCompletion polls the export, import, or acceptance counter every
// second until it reaches desiredCount. It returns an error on worker failure
// or timeout. The caller must still wait for workers; this function neither
// stops them nor interrupts in-flight ping retries.
func (r *raLatency) waitForSceanrioCompletion(desiredCount uint64, maxTimeout time.Duration, scenario string) error {
	var count uint64
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	timeoutTimer := time.NewTimer(maxTimeout)
	defer timeoutTimer.Stop()

	for {
		select {
		case err := <-r.scenarioErrCh:
			return fmt.Errorf("%s scenario failed: %w", scenario, err)
		case <-ticker.C:
			switch scenario {
			case "export":
				count = atomic.LoadUint64(&r.verifiedExportRouteCount)
			case "import":
				count = atomic.LoadUint64(&r.verifiedImportProbeCount)
			case "accepted":
				count = atomic.LoadUint64(&r.verifiedAcceptedCount)
			}
			log.Debugf("count %v , desiredCount %v", count, desiredCount)
			if count >= desiredCount {
				return nil
			}
		case <-timeoutTimer.C:
			log.Debugf("Timeout reached, signaling stop.")
			return fmt.Errorf("%s scenario timed out after %s: verified %d of %d operations", scenario, maxTimeout, count, desiredCount)
		}
	}
}

// Stop completes the enabled scenarios in order: export, then import.
// Each phase signals and waits for its workers before returning an error or moving on.
// Import interfaces are removed after workers exit. With no ping checks enabled,
// Stop waits for RA acceptance before normalizing metrics and checking thresholds.
func (r *raLatency) Stop() error {
	if r.JobConfig.SkipIndexing {
		return nil
	}

	// Export ping: wait + stop workers only if enabled
	if r.isExportEnabled() {
		log.Debugf("Export scenario is enabled")
		// Count unique CUDNs (workers verify once per CUDN, skipping additional subnets)
		uniqueCudns := make(map[string]struct{})
		for _, cpods := range r.cudnSubnet {
			uniqueCudns[cpods.cudn] = struct{}{}
		}
		desiredCount := uint64(len(uniqueCudns))
		exportErr := r.waitForSceanrioCompletion(desiredCount, r.JobConfig.MaxWaitTimeout, "export")
		close(r.exportDoneCh)
		r.wg.Wait()
		if exportErr != nil {
			return exportErr
		}
	} else {
		log.Debugf("Export scenario is not enabled")
		// When export is disabled, wait for RA acceptance before proceeding.
		// This allows BGP convergence before import (if enabled) and ensures acceptance
		// is measured even when no ping scenarios run.
		log.Debugf("Waiting for RA Accepted conditions")
		if err := r.waitForSceanrioCompletion(uint64(r.JobConfig.JobIterations), r.JobConfig.MaxWaitTimeout, "accepted"); err != nil {
			return err
		}
	}

	// Import ping: start + wait + stop only if enabled
	if r.isImportEnabled() {
		log.Debugf("Import scenario is enabled")
		if err := r.startImportScenario(); err != nil {
			return err
		}
		// Wait for all import probes: each route tests reachability to every CUDN
		// desiredCount = number of source routes × number of CUDNs to test
		desiredCount := uint64(importRoutesCount) * uint64(len(r.importTargets))
		importErr := r.waitForSceanrioCompletion(desiredCount, r.JobConfig.MaxWaitTimeout, "import")
		close(r.importDoneCh)
		r.wg.Wait()

		// Check for any import failures (zero tolerance policy - all probes must succeed)
		r.importLatencyMu.Lock()
		totalProbesPerCudn := importRoutesCount
		for cudn, aggregate := range r.importLatencyByCudn {
			if aggregate.failures > 0 {
				r.importLatencyMu.Unlock()
				return fmt.Errorf("import scenario failed: %d of %d probe(s) failed for CUDN %s (zero tolerance policy - all import probes must succeed)", aggregate.failures, totalProbesPerCudn, cudn)
			}
		}
		r.importLatencyMu.Unlock()

		// cleanup dummy interfaces only if import was run
		for i := range numDummyIfaces {
			if err := r.deleteDummyInterface(i); err != nil {
				log.Errorf("Error deleting dummy interface %d: %v", i, err)
			}
		}
		if importErr != nil {
			return importErr
		}
	} else {
		log.Debugf("Import scenario is not enabled")
	}

	return r.StopMeasurement(r.normalizeMetrics, r.getLatency)
}

func (r *raLatency) normalizeMetrics() float64 {
	r.Metrics.Range(func(key, value any) bool {
		m := value.(raMetric)
		m.ExportLatency = -1
		m.ImportLatency = -1
		m.NetlinkRouteLatency = -1
		if val, exists := r.cudnConnTimestamp.Load(m.cudn); exists {
			nlRouteVal := val.(netlinkRoutes)
			if !nlRouteVal.pingTimestamp.IsZero() {
				m.ExportLatency = float64(nlRouteVal.pingTimestamp.Sub(m.Timestamp).Milliseconds())
			}
			m.NetlinkRouteLatency = float64(nlRouteVal.routeTimestamp.Sub(m.Timestamp).Milliseconds())
		}
		r.importLatencyMu.Lock()
		if aggregate, exists := r.importLatencyByCudn[m.cudn]; exists {
			// maxLatency is -1 if no probes ran, or the maximum successful latency
			// With zero tolerance, if we reach this point, all probes succeeded
			m.ImportLatency = aggregate.maxLatency
			m.ImportRouteFailures = aggregate.failures
		}
		r.importLatencyMu.Unlock()
		log.Debugf("%s: acceptedLatency=%v ms exportLatency=%v ms importLatency=%v ms importRouteFailures=%d netlinkRouteLatency=%v ms", m.Name, m.AcceptedLatency, m.ExportLatency, m.ImportLatency, m.ImportRouteFailures, m.NetlinkRouteLatency)

		r.NormLatencies = append(r.NormLatencies, m)
		return true
	})
	return 0
}

func (r *raLatency) getLatency(normLatency any) map[string]float64 {
	m := normLatency.(raMetric)
	return map[string]float64{
		"AcceptedLatency":     m.AcceptedLatency,
		"ExportLatency":       m.ExportLatency,
		"ImportLatency":       m.ImportLatency,
		"NetlinkRouteLatency": m.NetlinkRouteLatency,
	}
}

func (r *raLatency) IsCompatible() bool {
	return slices.Contains(supportedRaLatencyJobTypes, r.JobConfig.JobType)
}
