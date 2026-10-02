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
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	k8sconnector "github.com/cloud-bulldozer/go-commons/v2/k8s-connector"
	"github.com/kube-burner/kube-burner-ocp/pkg/utils"
	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements/metrics"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements/types"
	"github.com/kube-burner/kube-burner/v2/pkg/util/fileutils"
	probing "github.com/prometheus-community/pro-bing"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

// raLatencyBCC measures end-to-end latency for BGP Cloud Connector (BCC) route advertisement from OpenShift to AWS VPC.
// It tracks five timestamps (T1-T5) for each BGPRouting CR and its associated pod:
//
// T1: Route appears in AWS Route Server RIB (Routing Information Base)
// T2: Route becomes active in AWS Route Server FIB (Forwarding Information Base)
// T3: Route appears in AWS VPC route table
// T4: Pod receives its CUDN IP address and enters Running state
// T5: Pod becomes network-reachable (first successful ICMP ping)
//
// Measurements are keyed by BGPRouting name and include per-subnet latencies along with
// aggregated statistics (min, max, avg, p99) for each timestamp. The T5_SuccessfulPings
// metric counts how many pods were successfully pinged (one per subnet).
const (
	raLatencyBCCMeasurement          = "raLatencyBCCMeasurement"
	raLatencyBCCQuantilesMeasurement = "raLatencyBCCQuantilesMeasurement"
)

var (
	maxTimeout           time.Duration = 1 * time.Minute
	awsRegion            string
	routeServerId        string
	routesPollInterval   time.Duration = 250 * time.Millisecond
	vpcId                string
	vpcRoutePollInterval time.Duration = 250 * time.Millisecond
	pingInterval         time.Duration = 100 * time.Millisecond

	supportedRaLatencyBCCJobTypes = []config.JobType{config.CreationJob, config.PatchJob}

	bgpRoutingGVR = schema.GroupVersionResource{
		Group:    "networking.openshift.io",
		Version:  "v1beta1",
		Resource: "bgproutings",
	}
)

type raMetricBCC struct {
	Timestamp  time.Time `json:"timestamp"`
	MetricName string    `json:"metricName"`
	UUID       string    `json:"uuid"`
	JobName    string    `json:"jobName,omitempty"`
	Name       string    `json:"bgpRoutingName"`
	Metadata   any       `json:"metadata,omitempty"`
	Subnets    []string  `json:"subnets"`

	T1_RibLatency []float64 `json:"t1_ribLatency,omitempty"`
	T1_MinLatency int       `json:"t1_minLatency,omitempty"`
	T1_MaxLatency int       `json:"t1_maxLatency,omitempty"`
	T1_AvgLatency int       `json:"t1_avgLatency,omitempty"`
	T1_P99Latency int       `json:"t1_p99Latency,omitempty"`

	T2_FibLatency []float64 `json:"t2_fibLatency,omitempty"`
	T2_MinLatency int       `json:"t2_minLatency,omitempty"`
	T2_MaxLatency int       `json:"t2_maxLatency,omitempty"`
	T2_AvgLatency int       `json:"t2_avgLatency,omitempty"`
	T2_P99Latency int       `json:"t2_p99Latency,omitempty"`

	T3_VpcRouteTableLatency []float64 `json:"t3_vpcRouteTableLatency,omitempty"`
	T3_MinLatency           int       `json:"t3_minLatency,omitempty"`
	T3_MaxLatency           int       `json:"t3_maxLatency,omitempty"`
	T3_AvgLatency           int       `json:"t3_avgLatency,omitempty"`
	T3_P99Latency           int       `json:"t3_p99Latency,omitempty"`

	T4_PodIPAssignedLatency []float64 `json:"t4_podIPAssignedLatency,omitempty"`
	T4_MinLatency           int       `json:"t4_minLatency,omitempty"`
	T4_MaxLatency           int       `json:"t4_maxLatency,omitempty"`
	T4_AvgLatency           int       `json:"t4_avgLatency,omitempty"`
	T4_P99Latency           int       `json:"t4_p99Latency,omitempty"`

	T5_PingSuccessLatency []float64 `json:"t5_pingSuccessLatency,omitempty"`
	T5_MinLatency         int       `json:"t5_minLatency,omitempty"`
	T5_MaxLatency         int       `json:"t5_maxLatency,omitempty"`
	T5_AvgLatency         int       `json:"t5_avgLatency,omitempty"`
	T5_P99Latency         int       `json:"t5_p99Latency,omitempty"`
	T5_SuccessfulPings    int       `json:"t5_successfulPings,omitempty"`
}

// detectedRoute holds route detection timestamps for each subnet.
type detectedRoute struct {
	subnet          string
	t1_RibTimestamp time.Time
	t2_FibTimestamp time.Time
	t3_VpcTimestamp time.Time
}

// podMeasurement holds pod lifecycle timestamps.
type podMeasurement struct {
	podIP            string
	subnet           string
	t4_PodIPAssigned time.Time
	t5_PingSuccess   time.Time
}

type podIPNotification struct {
	subnet    string
	podIP     string
	timestamp time.Time
}

// networkStatus represents a network interface from the k8s.v1.cni.cncf.io/network-status annotation.
type networkStatus struct {
	Name      string   `json:"name"`
	Interface string   `json:"interface"`
	IPs       []string `json:"ips"`
	Default   bool     `json:"default"`
}

type raLatencyBCC struct {
	measurements.BaseMeasurement

	routeTimestamps    sync.Map // key: subnet, value: *detectedRoute
	podTimestamps      sync.Map // key: subnet, value: *podMeasurement
	detectedRouteCount uint64
	doneCh             chan struct{}
	wg                 sync.WaitGroup
	podIPCh            chan podIPNotification
	connector          k8sconnector.K8SConnector
}

type raLatencyBCCMeasurementFactory struct {
	measurements.BaseMeasurementFactory
}

func NewRaLatencyBCCMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]any, labelSelector string) (measurements.MeasurementFactory, error) {
	return raLatencyBCCMeasurementFactory{
		measurements.NewBaseMeasurementFactory(configSpec, measurement, metadata, labelSelector),
	}, nil
}

func (plmf raLatencyBCCMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config, embedCfg *fileutils.EmbedConfiguration) measurements.Measurement {
	return &raLatencyBCC{
		BaseMeasurement: plmf.NewBaseLatency(jobConfig, clientSet, restConfig, raLatencyBCCMeasurement, raLatencyBCCQuantilesMeasurement, embedCfg),
	}
}

// handleAdd records BGPRouting creation timestamp and advertised subnets.
func (r *raLatencyBCC) handleAdd(obj any) {
	bgpRouting := obj.(*unstructured.Unstructured)

	bgpRoutingName, _, _ := unstructured.NestedString(bgpRouting.UnstructuredContent(), "metadata", "name")
	subnetsRaw, found, err := unstructured.NestedStringSlice(bgpRouting.UnstructuredContent(), "spec", "network", "subnets")
	if err != nil {
		log.Errorf("Error extracting subnets from BGPRouting %s: %v", bgpRoutingName, err)
		return
	}
	if !found || len(subnetsRaw) == 0 {
		log.Errorf("No subnets found in BGPRouting %s spec.network.subnets", bgpRoutingName)
		return
	}

	ts, _, _ := unstructured.NestedString(bgpRouting.UnstructuredContent(), "metadata", "creationTimestamp")
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		log.Errorf("Error parsing timestamp for BGPRouting %s: %v", bgpRoutingName, err)
		return
	}

	log.Debugf("BGPRouting %s (Subnets: %v) created at %v", bgpRoutingName, subnetsRaw, t.UTC())

	r.Metrics.LoadOrStore(bgpRoutingName, raMetricBCC{
		Name:                    bgpRoutingName,
		Timestamp:               t.UTC(),
		Subnets:                 subnetsRaw,
		T1_RibLatency:           []float64{},
		T2_FibLatency:           []float64{},
		T3_VpcRouteTableLatency: []float64{},
		T4_PodIPAssignedLatency: []float64{},
		T5_PingSuccessLatency:   []float64{},
		MetricName:              raLatencyBCCMeasurement,
		UUID:                    r.Uuid,
		Metadata:                r.Metadata,
		JobName:                 r.JobConfig.Name,
	})
}

type newRoute struct {
	Dst   string
	Ts    time.Time
	InFIB bool
}

func (r *raLatencyBCC) observerWorker(routeServer *utils.BCCRouteServerObserver, routeCh chan<- newRoute, baselineReadCh chan<- struct{}, vpcCheckCh chan<- struct{}) {
	defer r.wg.Done()

	baselineRead := false
	currentSubnets := make(map[string]bool)
	currentFibState := make(map[string]bool)

	read := func() {
		routes, err := routeServer.GetRoutes(context.Background())
		if err != nil {
			log.Warnf("BCC AWS Route Server query failed: %v", err)
			return
		}

		now := time.Now().UTC()
		fetchedSubnets := make(map[string]bool)
		fetchedFibState := make(map[string]bool)

		for _, route := range routes {
			subnetString := aws.ToString(route.Prefix)
			inFIB := route.RouteStatus == ec2types.RouteServerRouteStatusInFib

			fetchedSubnets[subnetString] = true
			fetchedFibState[subnetString] = inFIB

			wasInRib := currentSubnets[subnetString]
			wasFibActive := currentFibState[subnetString]

			if !wasInRib {
				// New route detected (T1)
				currentSubnets[subnetString] = true
				currentFibState[subnetString] = inFIB
				if baselineRead {
					log.Debugf("New route detected: %s (inFIB: %v)", subnetString, inFIB)
					routeCh <- newRoute{Dst: subnetString, Ts: now, InFIB: inFIB}
				} else {
					log.Debugf("Baseline route detected: %s", subnetString)
				}
			} else if baselineRead && !wasFibActive && inFIB {
				// FIB state changed from false to true (T2)
				currentFibState[subnetString] = inFIB
				log.Debugf("Route FIB state changed: %s now in-fib", subnetString)
				routeCh <- newRoute{Dst: subnetString, Ts: now, InFIB: inFIB}

				// Notify VPC worker to check immediately (non-blocking)
				select {
				case vpcCheckCh <- struct{}{}:
				default:
				}
			}
		}

		// Remove subnets that are no longer present in the fetched routes
		for subnet := range currentSubnets {
			if !fetchedSubnets[subnet] {
				delete(currentSubnets, subnet)
				delete(currentFibState, subnet)
				log.Debugf("Route removed: %s", subnet)
			}
		}
	}

	read() // Initial read to establish baseline
	baselineRead = true
	log.Debugf("Baseline read complete, monitoring for new routes...")
	close(baselineReadCh)

	ticker := time.NewTicker(routesPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			read()
		case <-r.doneCh:
			return
		}
	}
}

// worker records T1 (RIB) and T2 (FIB) timestamps as routes are advertised to AWS Route Server.
func (r *raLatencyBCC) worker(routeCh <-chan newRoute) {
	defer r.wg.Done()
	for {
		select {
		case update, ok := <-routeCh:
			if !ok {
				return
			}

			subnet := update.Dst
			now := update.Ts

			val, loaded := r.routeTimestamps.LoadOrStore(subnet, &detectedRoute{subnet: subnet})
			route := val.(*detectedRoute)

			// T1: Route first appears in RIB
			if !loaded && route.t1_RibTimestamp.IsZero() {
				route.t1_RibTimestamp = now
				log.Debugf("T1: Route %s in RIB at %v", subnet, now)
			}

			// T2: Route becomes in-fib (state = active)
			if update.InFIB && route.t2_FibTimestamp.IsZero() {
				route.t2_FibTimestamp = now
				log.Debugf("T2: Route %s in FIB at %v", subnet, now)
			}

			r.routeTimestamps.Store(subnet, route)

		case <-r.doneCh:
			return
		}
	}
}

// vpcRouteTableWorker detects T3 (route appears in VPC route table) by polling periodically and on demand.
func (r *raLatencyBCC) vpcRouteTableWorker(routeServer *utils.BCCRouteServerObserver, vpcCheckCh <-chan struct{}) {
	defer r.wg.Done()

	ticker := time.NewTicker(vpcRoutePollInterval)
	defer ticker.Stop()

	checkVpc := func() {
		// Collect all subnets that have T2 but not T3
		var pendingSubnets []string
		r.routeTimestamps.Range(func(key, value any) bool {
			subnet := key.(string)
			route := value.(*detectedRoute)

			// Only check routes that are in FIB but not yet in VPC table
			if !route.t2_FibTimestamp.IsZero() && route.t3_VpcTimestamp.IsZero() {
				pendingSubnets = append(pendingSubnets, subnet)
			}
			return true
		})

		if len(pendingSubnets) == 0 {
			return
		}

		// Single API call for all pending routes
		found, err := routeServer.CheckVpcRouteTables(context.Background(), vpcId, pendingSubnets)
		if err != nil {
			log.Warnf("VPC route table query failed: %v", err)
			return
		}

		// Update timestamps for routes found in VPC table
		now := time.Now().UTC()
		for subnet, exists := range found {
			if exists {
				val, ok := r.routeTimestamps.Load(subnet)
				if !ok {
					continue
				}
				route := val.(*detectedRoute)
				if route.t3_VpcTimestamp.IsZero() {
					route.t3_VpcTimestamp = now
					r.routeTimestamps.Store(subnet, route)
					log.Debugf("T3: Route %s found in VPC route table at %v", subnet, now)
					atomic.AddUint64(&r.detectedRouteCount, 1)
				}
			}
		}
	}

	for {
		select {
		case <-ticker.C:
			checkVpc()
		case <-vpcCheckCh:
			// Immediate check triggered by FIB state change
			checkVpc()
		case <-r.doneCh:
			return
		}
	}
}

// startMonitoring initializes AWS Route Server observers, VPC workers, and Kubernetes informers.
func (r *raLatencyBCC) startMonitoring() error {
	r.routeTimestamps = sync.Map{}

	routeServer, err := utils.NewBCCRouteServerObserver(awsRegion, routeServerId)
	if err != nil {
		log.Errorf("Failed to create route observer: %v", err)
		return err
	}

	// Start observer goroutine to poll AWS Route Server for route changes
	routeCh := make(chan newRoute, 1000)
	baselineReadCh := make(chan struct{})
	vpcCheckCh := make(chan struct{}, 100) // Buffered channel for VPC check notifications

	r.wg.Add(1)
	go r.observerWorker(routeServer, routeCh, baselineReadCh, vpcCheckCh)
	<-baselineReadCh // wait for baseline read to complete before starting workers

	// Start worker goroutine that may in the future do more than just record timestamps, e.g. validate routes in AWS Route Server
	// (and then we could have more than one worker)
	r.wg.Add(1)
	go r.worker(routeCh)

	// Start VPC route table worker (single worker for all routes)
	log.Infof("Starting VPC route table worker for VPC: %s", vpcId)
	r.wg.Add(1)
	go r.vpcRouteTableWorker(routeServer, vpcCheckCh)

	// Initialize pod IP channel and start T4 ping worker
	r.podIPCh = make(chan podIPNotification, 100)
	r.wg.Add(1)
	go r.t4PingWorker()
	log.Infof("Started T4 ping worker")

	log.Infof("Creating BGPRouting and Pod watchers for %s", r.JobConfig.Name)
	connector, err := k8sconnector.NewK8SConnector(r.RestConfig)
	if err != nil {
		log.Error(err)
		return err
	}
	r.connector = connector

	// BGPRouting informer
	bgpRoutingFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(r.connector.DynamicClient(), time.Minute, metav1.NamespaceAll, nil)
	bgpRoutingInformer := bgpRoutingFactory.ForResource(bgpRoutingGVR).Informer()
	bgpRoutingInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: r.handleAdd,
	})

	stopCh := make(chan struct{})
	bgpRoutingFactory.Start(stopCh)
	bgpRoutingFactory.WaitForCacheSync(stopCh)

	// Pod informer (filtered by kube-burner labels)
	podGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "pods",
	}
	podFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		r.connector.DynamicClient(),
		time.Minute,
		metav1.NamespaceAll,
		func(options *metav1.ListOptions) {
			options.LabelSelector = r.LabelSelector
		},
	)
	podInformer := podFactory.ForResource(podGVR).Informer()
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    r.handlePodAdd,
		UpdateFunc: r.handlePodUpdate,
	})

	stopCh = make(chan struct{})
	podFactory.Start(stopCh)
	podFactory.WaitForCacheSync(stopCh)

	return nil
}

// getCUDNIP extracts the CUDN IP from a pod's network-status annotation.
func getCUDNIP(pod *unstructured.Unstructured) (string, bool) {
	annotations, found, _ := unstructured.NestedStringMap(pod.UnstructuredContent(), "metadata", "annotations")
	if !found {
		return "", false
	}

	networkStatusJSON, ok := annotations["k8s.v1.cni.cncf.io/network-status"]
	if !ok {
		return "", false
	}

	// Parse the JSON array
	var networks []networkStatus
	if err := json.Unmarshal([]byte(networkStatusJSON), &networks); err != nil {
		log.Debugf("Failed to parse network-status annotation: %v", err)
		return "", false
	}

	// Find the CUDN interface (typically ovn-udn* and marked as default)
	for _, network := range networks {
		// Look for interface named ovn-udn* or marked as default (but not eth0)
		if network.Interface != "eth0" && (network.Default || len(network.IPs) > 0) {
			if len(network.IPs) > 0 {
				// Extract just the IP address (remove /prefix if present)
				ip := network.IPs[0]
				// Remove CIDR notation if present (e.g., "40.0.0.4/16" -> "40.0.0.4")
				for i := 0; i < len(ip); i++ {
					if ip[i] == '/' {
						ip = ip[:i]
						break
					}
				}
				log.Debugf("Found CUDN IP %s on interface %s", ip, network.Interface)
				return ip, true
			}
		}
	}

	return "", false
}

// recordT4 stores the T4 timestamp and notifies the ping worker.
func (r *raLatencyBCC) recordT4(subnet, podIP string, assignedTime time.Time) {
	// Load or create pod measurement keyed by subnet
	val, loaded := r.podTimestamps.LoadOrStore(subnet, &podMeasurement{
		podIP:            podIP,
		subnet:           subnet,
		t4_PodIPAssigned: assignedTime,
	})

	podMeas := val.(*podMeasurement)

	// If already loaded and T4 is already set, don't overwrite
	if loaded && !podMeas.t4_PodIPAssigned.IsZero() {
		log.Debugf("T4: Pod IP %s already recorded for subnet %s, skipping duplicate event", podIP, subnet)
		return
	}

	// First time recording T4 for this subnet
	if !loaded {
		log.Debugf("T4: Pod IP %s assigned from subnet %s at %v", podIP, subnet, assignedTime)

		// Send to ping worker
		select {
		case r.podIPCh <- podIPNotification{
			subnet:    subnet,
			podIP:     podIP,
			timestamp: assignedTime,
		}:
		default:
			log.Warnf("Pod IP channel full, dropping notification for %s", podIP)
		}
	}
}

// handlePodAdd tracks pods that reach Running state with an assigned IP.
func (r *raLatencyBCC) handlePodAdd(obj any) {
	r.handlePodEvent(obj)
}

func (r *raLatencyBCC) handlePodUpdate(oldObj, newObj any) {
	r.handlePodEvent(newObj)
}

func (r *raLatencyBCC) handlePodEvent(obj any) {
	pod := obj.(*unstructured.Unstructured)

	// Extract pod phase
	phase, _, _ := unstructured.NestedString(pod.UnstructuredContent(), "status", "phase")
	if phase != "Running" {
		return
	}

	// Extract CUDN IP from network-status annotation
	podIP, found := getCUDNIP(pod)
	if !found || podIP == "" {
		return
	}

	now := time.Now().UTC()

	// Find which subnet this IP belongs to and record T4
	foundSubnet := false
	r.Metrics.Range(func(key, value any) bool {
		metric := value.(raMetricBCC)

		for _, subnet := range metric.Subnets {
			if subnetContainsIP(subnet, podIP) {
				// Record T4 and start pinging
				r.recordT4(subnet, podIP, now)
				foundSubnet = true
				return false // Found the subnet, stop searching
			}
		}
		return true
	})
	if !foundSubnet {
		log.Warnf("Pod IP %s does not belong to any known subnet", podIP)
	}
}

// t4PingWorker dispatches ping goroutines for each pod and records T5 timestamps.
func (r *raLatencyBCC) t4PingWorker() {
	defer r.wg.Done()

	pingingIPs := make(map[string]bool)

	for {
		select {
		case notification := <-r.podIPCh:
			if pingingIPs[notification.podIP] {
				continue
			}

			pingingIPs[notification.podIP] = true
			r.wg.Add(1)
			go r.pingUntilReachable(notification.podIP, notification.subnet)

		case <-r.doneCh:
			return
		}
	}
}

func (r *raLatencyBCC) pingUntilReachable(podIP, subnet string) {
	defer r.wg.Done()
	log.Debugf("Starting ping of POD %s in subnet %s", podIP, subnet)

	ping := func() bool {
		if pingPodIP(podIP) {
			now := time.Now().UTC()
			log.Infof("T5: Pod %s reachable at %v", podIP, now)

			// Update T5 timestamp in pod measurements (keyed by subnet)
			val, ok := r.podTimestamps.Load(subnet)
			if !ok {
				log.Warnf("Pod measurement not found for subnet %s", subnet)
				return true
			}

			podMeas := val.(*podMeasurement)
			podMeas.t5_PingSuccess = now
			r.podTimestamps.Store(subnet, podMeas)
			log.Debugf("T5: Pod %s from subnet %s is pingable", podIP, subnet)
			return true
		}
		return false
	}

	if ping() {
		return
	}

	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	timeout := time.NewTimer(maxTimeout)
	defer timeout.Stop()

	for {
		select {
		case <-ticker.C:
			if ping() {
				return
			}
		case <-timeout.C:
			log.Warnf("T5: Timeout reached for pod %s", podIP)
			return
		case <-r.doneCh:
			log.Warnf("T5: Aborting ping for pod %s", podIP)
			return
		}
	}
}

// pingPodIP checks if a pod IP is reachable via ICMP.
func pingPodIP(ip string) bool {
	pinger, err := probing.NewPinger(ip)
	if err != nil {
		log.Debugf("Failed to create pinger for %s: %v", ip, err)
		return false
	}

	pinger.Count = 1
	pinger.Timeout = 1 * time.Second
	pinger.SetPrivileged(false)

	err = pinger.Run()
	if err != nil {
		return false
	}

	stats := pinger.Statistics()
	return stats.PacketsRecv > 0
}

// subnetContainsIP checks if a CIDR subnet contains an IP address.
func subnetContainsIP(cidr, ip string) bool {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}

	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}

	return network.Contains(parsedIP)
}

// setInputVars reads configuration from job template input variables.
func (r *raLatencyBCC) setInputVars() {
	var err error
	for _, obj := range r.JobConfig.Objects {
		if val, ok := obj.InputVars["maxTimeout"]; ok {
			maxTimeout, err = time.ParseDuration(val.(string))
			if err != nil {
				log.Errorf("Failure parsing maxTimeout: %v", err)
			}
		}
		if val, ok := obj.InputVars["awsRegion"]; ok {
			awsRegion = val.(string)
		}
		if val, ok := obj.InputVars["routeServerId"]; ok {
			routeServerId = val.(string)
		}
		if val, ok := obj.InputVars["vpcId"]; ok {
			vpcId = val.(string)
		}
	}
}

// Start initializes the BCC latency measurement.
func (r *raLatencyBCC) Start(measurementWg *sync.WaitGroup) error {
	r.LatencyQuantiles, r.NormLatencies = nil, nil
	r.Metrics = sync.Map{}

	defer measurementWg.Done()

	if r.JobConfig.SkipIndexing {
		return nil
	}
	r.setInputVars()

	r.doneCh = make(chan struct{})

	return r.startMonitoring()
}

// Collect is a no-op for this measurement.
func (r *raLatencyBCC) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

// waitForCompletion polls until all routes are detected or maxTimeout is reached.
func (r *raLatencyBCC) waitForCompletion(desiredCount uint64) {
	var count uint64
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	timeoutTimer := time.NewTimer(maxTimeout)
	defer timeoutTimer.Stop()

	for {
		select {
		case <-ticker.C:
			count = atomic.LoadUint64(&r.detectedRouteCount)
			log.Debugf("count %v , desiredCount %v", count, desiredCount)
			if count >= desiredCount {
				log.Debugf("Desired count reached, signaling stop.")
				// Give additional 60 seconds for threads to finish (pings after detecting routes)
				time.Sleep(60 * time.Second)
				return
			}
		case <-timeoutTimer.C:
			log.Debugf("Timeout reached, signaling stop.")
			return
		}
	}
}

// Stop waits for measurement completion and normalizes metrics.
func (r *raLatencyBCC) Stop() error {
	if r.JobConfig.SkipIndexing {
		return nil
	}

	var desiredCount uint64
	r.Metrics.Range(func(key, value any) bool {
		m := value.(raMetricBCC)
		desiredCount += uint64(len(m.Subnets))
		return true
	})

	log.Infof("Waiting for %d routes to be detected in AWS Route Server", desiredCount)
	r.waitForCompletion(desiredCount)

	close(r.doneCh)
	r.wg.Wait()

	return r.StopMeasurement(r.normalizeMetrics, r.getLatency)
}

func (r *raLatencyBCC) normalizeMetrics() float64 {
	r.Metrics.Range(func(key, value any) bool {
		m := value.(raMetricBCC)

		// For each subnet advertised by this BGPRouting, check if we detected it in AWS
		for _, subnet := range m.Subnets {
			val, exists := r.routeTimestamps.Load(subnet)
			if !exists {
				log.Warnf("BGPRouting %s advertised subnet %s but it was never detected", m.Name, subnet)
				continue
			}

			routeVal := val.(*detectedRoute)

			// Calculate T1 latency (RIB)
			if !routeVal.t1_RibTimestamp.IsZero() {
				latencyMs := float64(routeVal.t1_RibTimestamp.Sub(m.Timestamp).Milliseconds())
				m.T1_RibLatency = append(m.T1_RibLatency, latencyMs)
				log.Debugf("T1: BGPRouting %s subnet %s RIB latency: %.0f ms", m.Name, subnet, latencyMs)
			} else {
				log.Warnf("BGPRouting %s advertised subnet %s but it was never detected in RIB", m.Name, subnet)
			}

			// Calculate T2 latency (FIB)
			if !routeVal.t2_FibTimestamp.IsZero() {
				latencyMs := float64(routeVal.t2_FibTimestamp.Sub(m.Timestamp).Milliseconds())
				m.T2_FibLatency = append(m.T2_FibLatency, latencyMs)
				log.Debugf("T2: BGPRouting %s subnet %s FIB latency: %.0f ms", m.Name, subnet, latencyMs)
			} else {
				log.Warnf("BGPRouting %s advertised subnet %s but it was never detected in FIB", m.Name, subnet)
			}

			// Calculate T3 latency (VPC route table)
			if !routeVal.t3_VpcTimestamp.IsZero() {
				latencyMs := float64(routeVal.t3_VpcTimestamp.Sub(m.Timestamp).Milliseconds())
				m.T3_VpcRouteTableLatency = append(m.T3_VpcRouteTableLatency, latencyMs)
				log.Debugf("T3: BGPRouting %s subnet %s VPC table latency: %.0f ms", m.Name, subnet, latencyMs)
			} else {
				log.Warnf("BGPRouting %s advertised subnet %s but it was never detected in VPC route table", m.Name, subnet)
			}

			// Calculate T4 and T5 latencies from pod measurements
			podVal, podExists := r.podTimestamps.Load(subnet)
			if podExists {
				podMeas := podVal.(*podMeasurement)

				// T4: Pod IP assigned (relative to BGPRouting creation)
				if !podMeas.t4_PodIPAssigned.IsZero() {
					latencyMs := float64(podMeas.t4_PodIPAssigned.Sub(m.Timestamp).Milliseconds())
					m.T4_PodIPAssignedLatency = append(m.T4_PodIPAssignedLatency, latencyMs)
					log.Debugf("T4: BGPRouting %s subnet %s pod IP %s assigned latency: %.0f ms", m.Name, subnet, podMeas.podIP, latencyMs)
				}

				// T5: Ping success (relative to BGPRouting creation)
				if !podMeas.t5_PingSuccess.IsZero() {
					latencyMs := float64(podMeas.t5_PingSuccess.Sub(m.Timestamp).Milliseconds())
					m.T5_PingSuccessLatency = append(m.T5_PingSuccessLatency, latencyMs)
					log.Debugf("T5: BGPRouting %s subnet %s pod IP %s ping success latency: %.0f ms", m.Name, subnet, podMeas.podIP, latencyMs)
				}
			} else {
				log.Warnf("BGPRouting %s advertised subnet %s but no pod was found", m.Name, subnet)
			}
		}

		// Calculate T1 statistics
		if len(m.T1_RibLatency) > 0 {
			t1Summary := metrics.NewLatencySummary(m.T1_RibLatency, m.Name+"-T1")
			m.T1_MinLatency = t1Summary.Min
			m.T1_MaxLatency = t1Summary.Max
			m.T1_AvgLatency = t1Summary.Avg
			m.T1_P99Latency = t1Summary.P99
			log.Infof("T1 (RIB) %s - Min: %dms, Max: %dms, Avg: %dms, P99: %dms",
				m.Name, t1Summary.Min, t1Summary.Max, t1Summary.Avg, t1Summary.P99)
		}

		// Calculate T2 statistics
		if len(m.T2_FibLatency) > 0 {
			t2Summary := metrics.NewLatencySummary(m.T2_FibLatency, m.Name+"-T2")
			m.T2_MinLatency = t2Summary.Min
			m.T2_MaxLatency = t2Summary.Max
			m.T2_AvgLatency = t2Summary.Avg
			m.T2_P99Latency = t2Summary.P99
			log.Infof("T2 (FIB) %s - Min: %dms, Max: %dms, Avg: %dms, P99: %dms",
				m.Name, t2Summary.Min, t2Summary.Max, t2Summary.Avg, t2Summary.P99)
		}

		// Calculate T3 statistics
		if len(m.T3_VpcRouteTableLatency) > 0 {
			t3Summary := metrics.NewLatencySummary(m.T3_VpcRouteTableLatency, m.Name+"-T3")
			m.T3_MinLatency = t3Summary.Min
			m.T3_MaxLatency = t3Summary.Max
			m.T3_AvgLatency = t3Summary.Avg
			m.T3_P99Latency = t3Summary.P99
			log.Infof("T3 (VPC) %s - Min: %dms, Max: %dms, Avg: %dms, P99: %dms",
				m.Name, t3Summary.Min, t3Summary.Max, t3Summary.Avg, t3Summary.P99)
		}

		// Calculate T4 statistics
		if len(m.T4_PodIPAssignedLatency) > 0 {
			t4Summary := metrics.NewLatencySummary(m.T4_PodIPAssignedLatency, m.Name+"-T4")
			m.T4_MinLatency = t4Summary.Min
			m.T4_MaxLatency = t4Summary.Max
			m.T4_AvgLatency = t4Summary.Avg
			m.T4_P99Latency = t4Summary.P99
			log.Infof("T4 (Pod IP Assigned) %s - Min: %dms, Max: %dms, Avg: %dms, P99: %dms",
				m.Name, t4Summary.Min, t4Summary.Max, t4Summary.Avg, t4Summary.P99)
		}

		// Calculate T5 statistics
		if len(m.T5_PingSuccessLatency) > 0 {
			t5Summary := metrics.NewLatencySummary(m.T5_PingSuccessLatency, m.Name+"-T5")
			m.T5_MinLatency = t5Summary.Min
			m.T5_MaxLatency = t5Summary.Max
			m.T5_AvgLatency = t5Summary.Avg
			m.T5_P99Latency = t5Summary.P99
			m.T5_SuccessfulPings = len(m.T5_PingSuccessLatency)
			log.Infof("T5 (Ping Success) %s - Min: %dms, Max: %dms, Avg: %dms, P99: %dms, Successful: %d",
				m.Name, t5Summary.Min, t5Summary.Max, t5Summary.Avg, t5Summary.P99, m.T5_SuccessfulPings)
		}

		r.NormLatencies = append(r.NormLatencies, m)
		return true
	})
	return 0
}

func (r *raLatencyBCC) getLatency(normLatency any) map[string]float64 {
	raMetric := normLatency.(raMetricBCC)
	return map[string]float64{
		// T1: RIB
		"T1_MinLatency": float64(raMetric.T1_MinLatency),
		"T1_MaxLatency": float64(raMetric.T1_MaxLatency),
		"T1_AvgLatency": float64(raMetric.T1_AvgLatency),
		"T1_P99Latency": float64(raMetric.T1_P99Latency),
		// T2: FIB
		"T2_MinLatency": float64(raMetric.T2_MinLatency),
		"T2_MaxLatency": float64(raMetric.T2_MaxLatency),
		"T2_AvgLatency": float64(raMetric.T2_AvgLatency),
		"T2_P99Latency": float64(raMetric.T2_P99Latency),
		// T3: VPC Route Table
		"T3_MinLatency": float64(raMetric.T3_MinLatency),
		"T3_MaxLatency": float64(raMetric.T3_MaxLatency),
		"T3_AvgLatency": float64(raMetric.T3_AvgLatency),
		"T3_P99Latency": float64(raMetric.T3_P99Latency),
		// T4: Pod IP Assigned
		"T4_MinLatency": float64(raMetric.T4_MinLatency),
		"T4_MaxLatency": float64(raMetric.T4_MaxLatency),
		"T4_AvgLatency": float64(raMetric.T4_AvgLatency),
		"T4_P99Latency": float64(raMetric.T4_P99Latency),
		// T5: Ping Success
		"T5_MinLatency":      float64(raMetric.T5_MinLatency),
		"T5_MaxLatency":      float64(raMetric.T5_MaxLatency),
		"T5_AvgLatency":      float64(raMetric.T5_AvgLatency),
		"T5_P99Latency":      float64(raMetric.T5_P99Latency),
		"T5_SuccessfulPings": float64(raMetric.T5_SuccessfulPings),
	}
}

func (r *raLatencyBCC) IsCompatible() bool {
	return slices.Contains(supportedRaLatencyBCCJobTypes, r.JobConfig.JobType)
}
