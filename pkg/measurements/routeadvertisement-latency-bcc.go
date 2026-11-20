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
	"github.com/kube-burner/kube-burner-ocp/pkg/utils"
	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements"
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

// raLatencyBCC measures end-to-end latency for BGP Cloud Connector (BCC) route advertisement from OpenShift to cloud route servers.
// Supports AWS VPC Route Server and Azure Route Server. It tracks up to nine timestamps (T0-T5) for each BGPRouting CR:
//
// T0: BGPRouting condition NetworkCreated becomes True (the BGP Cloud Connector created its CUDN)
// T0.5: BGPRouting condition RouteAdvertisementsCreated becomes True (the shared RouteAdvertisements exist)
// T1: Route appears in the Route Server RIB (AWS routing database / learned routes of the first Azure BGP peer)
// T1.5: Route appears in the RIB from every BGP peer (AWS routing database entries / Azure learned routes)
// T2: Route becomes active for forwarding (AWS Route Server FIB / Azure NIC effective routes)
// T3: Route appears in the cloud route table (AWS VPC route tables / effective routes of the first Azure BGP peer NIC)
// T3.5: Route appears in all route tables (AWS: route server installation status / Azure: every BGP peer NIC)
// T4: Pod receives its CUDN IP address and enters Running state
// T5: Pod becomes network-reachable (first successful ICMP ping)
//
// Measurements are keyed by BGPRouting name and hold, per timestamp, the latency of each of its subnets.
// kube-burner aggregates them across BGPRoutings into one quantile per timestamp.
// Azure exposes no Route Server FIB state, so T2 is recorded together with T3.
const (
	raLatencyBCCMeasurement          = "raLatencyBCCMeasurement"
	raLatencyBCCQuantilesMeasurement = "raLatencyBCCQuantilesMeasurement"
	conditionStatusTrue              = "True"
	conditionNetworkCreated          = "NetworkCreated"
	conditionRACreated               = "RouteAdvertisementsCreated"
)

const (
	routePollInterval = 250 * time.Millisecond
	// The baseline read is retried a few times, since one failed peer query would abort the run
	baselineAttempts   = 3
	baselineRetryDelay = 5 * time.Second
	pingInterval       = 100 * time.Millisecond
)

var (
	supportedRaLatencyBCCJobTypes = []config.JobType{config.CreationJob, config.PatchJob}

	// BGPRoutingGVR identifies the BGP Cloud Connector's BGPRouting CRs.
	BGPRoutingGVR = schema.GroupVersionResource{
		Group:    "networking.openshift.io",
		Version:  "v1beta1",
		Resource: "bgproutings",
	}
	podGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
)

type raMetricBCC struct {
	Timestamp      time.Time `json:"timestamp"`
	MetricName     string    `json:"metricName"`
	UUID           string    `json:"uuid"`
	JobName        string    `json:"jobName,omitempty"`
	BGPRoutingName string    `json:"bgpRoutingName"`
	Metadata       any       `json:"metadata,omitempty"`
	Subnets        []string  `json:"subnets"`
	// Latencies holds, per timestamp (T0, T0_5, ... T5), the latency in ms of each subnet that reached it
	Latencies map[string][]float64 `json:"latencies"`
}

// detectedRoute holds route detection timestamps for each subnet.
type detectedRoute struct {
	t0_NetworkCreated time.Time
	t0_5_RACreated    time.Time
	t1_Rib            time.Time
	t1_5_AllPeers     time.Time
	t2_Fib            time.Time
	t3_RouteTable     time.Time
	t3_5_AllTables    time.Time
}

// podMeasurement holds pod lifecycle timestamps.
type podMeasurement struct {
	t4_PodIPAssigned time.Time
	t5_PingSuccess   time.Time
}

type podIPNotification struct {
	subnet string
	podIP  string
}

// networkStatus represents a network interface from the k8s.v1.cni.cncf.io/network-status annotation.
type networkStatus struct {
	Interface string   `json:"interface"`
	IPs       []string `json:"ips"`
}

type raLatencyBCC struct {
	measurements.BaseMeasurement

	routeProvider utils.BCCRouteServerProvider // created and validated by the workload
	maxTimeout    time.Duration

	routeTimes         map[string]*detectedRoute // key: subnet
	routeMu            sync.Mutex                // guards routeTimes and its entries, written by several goroutines
	podTimes           sync.Map                  // key: subnet, value: *podMeasurement
	detectedRouteCount atomic.Uint64
	finishedPingCount  atomic.Uint64 // pings that succeeded (T5) or timed out
	doneCh             chan struct{}
	wg                 sync.WaitGroup
	podIPCh            chan podIPNotification
	informerStopCh     chan struct{} // stops the BGPRouting and pod watchers
}

type raLatencyBCCMeasurementFactory struct {
	measurements.BaseMeasurementFactory
	routeProvider utils.BCCRouteServerProvider
}

// NewRaLatencyBCCMeasurementFactory returns a factory whose measurements observe routeProvider. The workload
// creates and validates the provider before the run, so the Route Server peers are discovered only once.
func NewRaLatencyBCCMeasurementFactory(routeProvider utils.BCCRouteServerProvider) measurements.NewMeasurementFactory {
	return func(configSpec config.Spec, measurement types.Measurement, metadata map[string]any, labelSelector string) (measurements.MeasurementFactory, error) {
		return raLatencyBCCMeasurementFactory{
			BaseMeasurementFactory: measurements.NewBaseMeasurementFactory(configSpec, measurement, metadata, labelSelector),
			routeProvider:          routeProvider,
		}, nil
	}
}

func (f raLatencyBCCMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config, embedCfg *fileutils.EmbedConfiguration) measurements.Measurement {
	return &raLatencyBCC{
		BaseMeasurement: f.NewBaseLatency(jobConfig, clientSet, restConfig, raLatencyBCCMeasurement, raLatencyBCCQuantilesMeasurement, embedCfg),
		routeProvider:   f.routeProvider,
	}
}

// handleBGPRoutingAdd records BGPRouting creation timestamp and advertised subnets.
func (r *raLatencyBCC) handleBGPRoutingAdd(obj any) {
	bgpRouting := obj.(*unstructured.Unstructured)

	bgpRoutingName, _, _ := unstructured.NestedString(bgpRouting.UnstructuredContent(), "metadata", "name")
	subnets, found, err := unstructured.NestedStringSlice(bgpRouting.UnstructuredContent(), "spec", "network", "subnets")
	if err != nil {
		log.Errorf("Error extracting subnets from BGPRouting %s: %v", bgpRoutingName, err)
		return
	}
	if !found || len(subnets) == 0 {
		log.Errorf("No subnets found in BGPRouting %s spec.network.subnets", bgpRoutingName)
		return
	}

	created, _, _ := unstructured.NestedString(bgpRouting.UnstructuredContent(), "metadata", "creationTimestamp")
	createdAt, err := time.Parse(time.RFC3339, created)
	if err != nil {
		log.Errorf("Error parsing timestamp for BGPRouting %s: %v", bgpRoutingName, err)
		return
	}
	createdAt = createdAt.UTC()

	log.Debugf("BGPRouting %s (Subnets: %v) created at %v", bgpRoutingName, subnets, createdAt)

	r.Metrics.LoadOrStore(bgpRoutingName, raMetricBCC{
		BGPRoutingName: bgpRoutingName,
		Timestamp:      createdAt,
		Subnets:        subnets,
		MetricName:     raLatencyBCCMeasurement,
		UUID:           r.Uuid,
		Metadata:       r.Metadata,
		JobName:        r.JobConfig.Name,
	})
}

// handleBGPRoutingUpdate records T0 and T0.5 for the BGPRouting's subnets when the BGP Cloud Connector
// sets the NetworkCreated and RouteAdvertisementsCreated conditions to True.
func (r *raLatencyBCC) handleBGPRoutingUpdate(_, newObj any) {
	bgpRouting := newObj.(*unstructured.Unstructured)
	subnets, _, _ := unstructured.NestedStringSlice(bgpRouting.UnstructuredContent(), "spec", "network", "subnets")
	conditions, _, _ := unstructured.NestedSlice(bgpRouting.UnstructuredContent(), "status", "conditions")
	now := time.Now().UTC()

	r.routeMu.Lock()
	defer r.routeMu.Unlock()
	for _, c := range conditions {
		cond, ok := c.(map[string]any)
		if !ok || cond["status"] != conditionStatusTrue {
			continue
		}
		for _, subnet := range subnets {
			route := r.route(subnet)
			switch cond["type"] {
			case conditionNetworkCreated:
				if route.t0_NetworkCreated.IsZero() {
					route.t0_NetworkCreated = now
					log.Debugf("T0: Network for subnet %s created at %v", subnet, now)
				}
			case conditionRACreated:
				if route.t0_5_RACreated.IsZero() {
					route.t0_5_RACreated = now
					log.Debugf("T0.5: RouteAdvertisements for subnet %s created at %v", subnet, now)
				}
			}
		}
	}
}

// routeServerWorker polls the Route Server and records T1, T1.5 and T2. Routes already present in the
// baseline read predate the measurement and are ignored until they disappear.
func (r *raLatencyBCC) routeServerWorker(baselineReadCh chan<- error, routeTableCheckCh chan<- struct{}) {
	defer r.wg.Done()

	baseline := map[string]bool{}
	read := func(isBaseline bool) error {
		routes, err := r.routeProvider.GetRoutes(context.Background())
		if err != nil {
			log.Warnf("BCC Route Server query failed: %v", err)
			return err
		}
		now := time.Now().UTC()
		present := map[string]bool{}

		r.routeMu.Lock()
		defer r.routeMu.Unlock()
		for _, route := range routes {
			present[route.Prefix] = true
			if isBaseline {
				baseline[route.Prefix] = true
				continue
			}
			if baseline[route.Prefix] {
				continue
			}
			if r.recordRouteServer(route, now) {
				// T2 just set: let the route table worker check right away (non-blocking)
				select {
				case routeTableCheckCh <- struct{}{}:
				default:
				}
			}
		}
		for prefix := range baseline {
			if !present[prefix] {
				delete(baseline, prefix)
			}
		}
		return nil
	}

	// Without a complete baseline, routes that already existed would be recorded as new
	var err error
	for attempt := 1; attempt <= baselineAttempts; attempt++ {
		if err = read(true); err == nil {
			break
		}
		if attempt < baselineAttempts {
			time.Sleep(baselineRetryDelay)
		}
	}
	baselineReadCh <- err
	if err != nil {
		return
	}
	log.Debugf("Baseline read complete, monitoring for new routes...")

	ticker := time.NewTicker(routePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if r.routeServerPending() {
				read(false)
			}
		case <-r.doneCh:
			return
		}
	}
}

// recordRouteServer records T1, T1.5 and T2 for a route seen in the Route Server and reports whether T2 was
// just set. Providers that stamp each peer report when the route was first seen; otherwise the poll time is used.
// Callers hold routeMu.
func (r *raLatencyBCC) recordRouteServer(route utils.RouteInfo, now time.Time) (t2Set bool) {
	d := r.route(route.Prefix)

	if d.t1_Rib.IsZero() {
		d.t1_Rib = now
		if !route.FirstSeen.IsZero() {
			d.t1_Rib = route.FirstSeen
		}
		log.Debugf("T1: Route %s in RIB at %v", route.Prefix, d.t1_Rib)
	}

	// A route completes once it has both T1.5 and T3.5, which come from independent queries
	if !route.AllSeen.IsZero() && d.t1_5_AllPeers.IsZero() {
		d.t1_5_AllPeers = route.AllSeen
		log.Debugf("T1.5: Route %s in RIB from all BGP peers at %v", route.Prefix, route.AllSeen)
		if !d.t3_5_AllTables.IsZero() {
			r.detectedRouteCount.Add(1)
		}
	}

	if route.InFIB && d.t2_Fib.IsZero() {
		d.t2_Fib = now
		log.Debugf("T2: Route %s in FIB at %v", route.Prefix, now)
		return true
	}
	return false
}

// routeServerPending reports whether any BGPRouting subnet still needs a Route Server observation:
// T1 and T1.5, plus T2 and T3.5 when the Route Server reports FIB and installation state (AWS).
// Polling stops otherwise, to avoid cloud API throttling.
func (r *raLatencyBCC) routeServerPending() bool {
	reportsFIB := r.routeProvider.ReportsFIB()
	r.routeMu.Lock()
	defer r.routeMu.Unlock()
	pending := false
	r.Metrics.Range(func(_, value any) bool {
		for _, subnet := range value.(raMetricBCC).Subnets {
			route, ok := r.routeTimes[subnet]
			if !ok {
				pending = true
				return false
			}
			awaitingFIB := reportsFIB && route.t2_Fib.IsZero()
			awaitingInstall := reportsFIB && route.t3_5_AllTables.IsZero()
			if route.t1_Rib.IsZero() || route.t1_5_AllPeers.IsZero() || awaitingFIB || awaitingInstall {
				pending = true
				return false
			}
		}
		return true
	})
	return pending
}

// routeTableWorker detects T3 (route appears in route table) by polling periodically and on demand.
func (r *raLatencyBCC) routeTableWorker(routeTableCheckCh <-chan struct{}) {
	defer r.wg.Done()

	ticker := time.NewTicker(routePollInterval)
	defer ticker.Stop()
	reportsFIB := r.routeProvider.ReportsFIB()

	checkRouteTable := func() {
		// Collect subnets still waiting for T3.5. With FIB state (AWS) only routes already in FIB (T2)
		// are checked; without it (Azure) every tracked route is.
		var pendingSubnets []string
		r.routeMu.Lock()
		for subnet, route := range r.routeTimes {
			if route.t3_5_AllTables.IsZero() && (!reportsFIB || !route.t2_Fib.IsZero()) {
				pendingSubnets = append(pendingSubnets, subnet)
			}
		}
		r.routeMu.Unlock()

		if len(pendingSubnets) == 0 {
			return
		}

		// One provider call covers all pending subnets
		found, err := r.routeProvider.CheckRouteTables(context.Background(), pendingSubnets)
		if err != nil {
			log.Warnf("route table query failed: %v", err)
			return
		}

		// Update timestamps for routes found in route table, using the time the provider first saw them.
		// A route counts as detected once it has both T3.5 and T1.5.
		r.routeMu.Lock()
		defer r.routeMu.Unlock()
		for subnet, status := range found {
			if status.FirstSeen.IsZero() {
				continue
			}
			route, ok := r.routeTimes[subnet]
			if !ok {
				continue
			}
			if route.t3_RouteTable.IsZero() {
				if !reportsFIB && route.t2_Fib.IsZero() {
					route.t2_Fib = status.FirstSeen
				}
				route.t3_RouteTable = status.FirstSeen
				log.Debugf("T3: Route %s found in route table at %v", subnet, status.FirstSeen)
			}
			if !status.AllSeen.IsZero() && route.t3_5_AllTables.IsZero() {
				// On AWS, T3.5 comes from the route server's installation status and T3 from the route tables;
				// never report the route in all route tables before it was seen in the first one
				route.t3_5_AllTables = status.AllSeen
				if route.t3_5_AllTables.Before(route.t3_RouteTable) {
					route.t3_5_AllTables = route.t3_RouteTable
				}
				log.Debugf("T3.5: Route %s found in all route tables at %v", subnet, route.t3_5_AllTables)
				if !route.t1_5_AllPeers.IsZero() {
					r.detectedRouteCount.Add(1)
				}
			}
		}
	}

	for {
		select {
		case <-ticker.C:
			checkRouteTable()
		case <-routeTableCheckCh:
			// Immediate check triggered by FIB state change
			checkRouteTable()
		case <-r.doneCh:
			return
		}
	}
}

// route returns the detection timestamps of subnet, adding an empty entry on first use. Callers hold routeMu.
func (r *raLatencyBCC) route(subnet string) *detectedRoute {
	d, ok := r.routeTimes[subnet]
	if !ok {
		d = &detectedRoute{}
		r.routeTimes[subnet] = d
	}
	return d
}

// startMonitoring starts the Route Server workers and the Kubernetes watchers.
func (r *raLatencyBCC) startMonitoring() error {
	r.routeTimes = map[string]*detectedRoute{}

	if r.routeProvider == nil {
		return fmt.Errorf("no Route Server provider configured")
	}
	// The provider is shared with the workload's validation and earlier jobs; their sightings could
	// belong to routes of a previous run that are still being withdrawn, and would corrupt T1/T1.5/T3.5
	r.routeProvider.Reset()

	baselineReadCh := make(chan error, 1)
	// One pending signal is enough: a single check covers every pending subnet, so further signals are dropped
	routeTableCheckCh := make(chan struct{}, 1)
	r.wg.Add(1)
	go r.routeServerWorker(baselineReadCh, routeTableCheckCh)
	if err := <-baselineReadCh; err != nil {
		r.wg.Wait()
		return fmt.Errorf("failed to read initial Route Server baseline: %w", err)
	}

	log.Infof("Starting route table worker")
	r.wg.Add(1)
	go r.routeTableWorker(routeTableCheckCh)

	r.podIPCh = make(chan podIPNotification, 100)
	r.wg.Add(1)
	go r.pingWorker()

	log.Infof("Creating BGPRouting and Pod watchers for %s", r.JobConfig.Name)
	connector, err := k8sconnector.NewK8SConnector(r.RestConfig)
	if err != nil {
		return fmt.Errorf("creating Kubernetes connector: %w", err)
	}
	r.informerStopCh = make(chan struct{})

	// BGPRouting informer: creation timestamp, plus T0/T0.5 from status conditions
	bgpRoutingFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(connector.DynamicClient(), time.Minute, metav1.NamespaceAll, nil)
	bgpRoutingInformer := bgpRoutingFactory.ForResource(BGPRoutingGVR).Informer()
	bgpRoutingInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    r.handleBGPRoutingAdd,
		UpdateFunc: r.handleBGPRoutingUpdate,
	})

	bgpRoutingFactory.Start(r.informerStopCh)
	bgpRoutingFactory.WaitForCacheSync(r.informerStopCh)

	// Pod informer (filtered by kube-burner labels)
	podFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		connector.DynamicClient(),
		time.Minute,
		metav1.NamespaceAll,
		func(options *metav1.ListOptions) {
			options.LabelSelector = r.LabelSelector
		},
	)
	podInformer := podFactory.ForResource(podGVR).Informer()
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    r.handlePodEvent,
		UpdateFunc: func(_, newObj any) { r.handlePodEvent(newObj) },
	})
	podFactory.Start(r.informerStopCh)
	podFactory.WaitForCacheSync(r.informerStopCh)

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

	var networks []networkStatus
	if err := json.Unmarshal([]byte(networkStatusJSON), &networks); err != nil {
		log.Debugf("Failed to parse network-status annotation: %v", err)
		return "", false
	}

	// The CUDN interface is the first one besides eth0 that has an IP (typically ovn-udn*)
	for _, network := range networks {
		if network.Interface == "eth0" || len(network.IPs) == 0 {
			continue
		}
		ip, _, _ := strings.Cut(network.IPs[0], "/") // e.g. "40.0.0.4/16" -> "40.0.0.4"
		log.Debugf("Found CUDN IP %s on interface %s", ip, network.Interface)
		return ip, true
	}

	return "", false
}

// recordT4 stores T4 the first time the subnet's pod is running with its CUDN IP, and starts pinging it.
// Later events for the same pod (updates, watcher resyncs) are ignored.
func (r *raLatencyBCC) recordT4(subnet, podIP string, assignedTime time.Time) {
	if _, loaded := r.podTimes.LoadOrStore(subnet, &podMeasurement{t4_PodIPAssigned: assignedTime}); loaded {
		return
	}
	log.Debugf("T4: Pod IP %s assigned from subnet %s at %v", podIP, subnet, assignedTime)
	select {
	case r.podIPCh <- podIPNotification{subnet: subnet, podIP: podIP}:
	default:
		log.Warnf("Pod IP channel full, dropping notification for %s", podIP)
	}
}

// handlePodEvent records T4 for pods that reach Running state with a CUDN IP.
func (r *raLatencyBCC) handlePodEvent(obj any) {
	pod := obj.(*unstructured.Unstructured)

	phase, _, _ := unstructured.NestedString(pod.UnstructuredContent(), "status", "phase")
	if phase != "Running" {
		return
	}

	podIP, found := getCUDNIP(pod)
	if !found || podIP == "" {
		return
	}

	now := time.Now().UTC()

	// Find which subnet this IP belongs to and record T4
	foundSubnet := false
	r.Metrics.Range(func(_, value any) bool {
		metric := value.(raMetricBCC)

		for _, subnet := range metric.Subnets {
			if subnetContainsIP(subnet, podIP) {
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

// pingWorker starts a ping goroutine for each pod that reached T4; the pings record T5.
func (r *raLatencyBCC) pingWorker() {
	defer r.wg.Done()
	for {
		select {
		case notification := <-r.podIPCh:
			r.wg.Add(1)
			go r.pingUntilReachable(notification.podIP, notification.subnet)
		case <-r.doneCh:
			return
		}
	}
}

// pingUntilReachable pings the pod right away and then every pingInterval until it answers (T5),
// maxTimeout passes, or the measurement stops.
func (r *raLatencyBCC) pingUntilReachable(podIP, subnet string) {
	defer r.wg.Done()
	defer r.finishedPingCount.Add(1)
	log.Debugf("Starting ping of POD %s in subnet %s", podIP, subnet)

	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	timeout := time.NewTimer(r.maxTimeout)
	defer timeout.Stop()

	for {
		if pingPodIP(podIP) {
			now := time.Now().UTC()
			val, _ := r.podTimes.Load(subnet) // stored by recordT4 before the ping started
			val.(*podMeasurement).t5_PingSuccess = now
			log.Infof("T5: Pod %s reachable at %v", podIP, now)
			return
		}
		select {
		case <-ticker.C:
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

	if err := pinger.Run(); err != nil {
		return false
	}
	return pinger.Statistics().PacketsRecv > 0
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

// setMaxTimeout reads maxTimeout from the input variables of the job's objects (default 1m).
func (r *raLatencyBCC) setMaxTimeout() {
	r.maxTimeout = time.Minute
	for _, obj := range r.JobConfig.Objects {
		val, ok := obj.InputVars["maxTimeout"].(string)
		if !ok {
			continue
		}
		timeout, err := time.ParseDuration(val)
		if err != nil {
			log.Errorf("Failure parsing maxTimeout: %v", err)
			continue
		}
		r.maxTimeout = timeout
	}
}

// Start initializes the BCC latency measurement.
func (r *raLatencyBCC) Start(measurementWg *sync.WaitGroup) error {
	defer measurementWg.Done()
	r.LatencyQuantiles, r.NormLatencies = nil, nil
	r.Metrics = sync.Map{}
	if r.JobConfig.SkipIndexing {
		return nil
	}
	r.setMaxTimeout()
	r.doneCh = make(chan struct{})
	return r.startMonitoring()
}

// Collect is a no-op for this measurement.
func (r *raLatencyBCC) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

// waitForCompletion returns once every subnet's route is detected and its pod's ping has finished
// (succeeded or timed out), or when maxTimeout is reached. The workload creates one pod per subnet.
func (r *raLatencyBCC) waitForCompletion(desiredCount uint64) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	timeoutTimer := time.NewTimer(r.maxTimeout)
	defer timeoutTimer.Stop()

	for {
		select {
		case <-ticker.C:
			if r.detectedRouteCount.Load() >= desiredCount && r.finishedPingCount.Load() >= desiredCount {
				log.Debugf("All %d routes detected and pings finished, signaling stop", desiredCount)
				return
			}
		case <-timeoutTimer.C:
			log.Warnf("Timeout reached with %d/%d routes detected and %d/%d pings finished",
				r.detectedRouteCount.Load(), desiredCount, r.finishedPingCount.Load(), desiredCount)
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
	r.Metrics.Range(func(_, value any) bool {
		m := value.(raMetricBCC)
		desiredCount += uint64(len(m.Subnets))
		return true
	})

	log.Infof("Waiting for %d routes to be detected and their pod pings to finish", desiredCount)
	r.waitForCompletion(desiredCount)

	close(r.doneCh)
	close(r.informerStopCh)
	r.wg.Wait()

	return r.StopMeasurement(r.normalizeMetrics, r.getLatency)
}

// milestone is one reported timestamp of a subnet; name is its latency and quantile key.
type milestone struct {
	name string
	at   time.Time
}

// milestones returns a subnet's timestamps in reporting order.
func milestones(route detectedRoute, pod podMeasurement) []milestone {
	return []milestone{
		{"T0", route.t0_NetworkCreated},
		{"T0_5", route.t0_5_RACreated},
		{"T1", route.t1_Rib},
		{"T1_5", route.t1_5_AllPeers},
		{"T2", route.t2_Fib},
		{"T3", route.t3_RouteTable},
		{"T3_5", route.t3_5_AllTables},
		{"T4", pod.t4_PodIPAssigned},
		{"T5", pod.t5_PingSuccess},
	}
}

// normalizeMetrics converts each subnet's timestamps into latencies relative to its BGPRouting's creation.
func (r *raLatencyBCC) normalizeMetrics() float64 {
	var subnets, pings int
	r.Metrics.Range(func(_, value any) bool {
		m := value.(raMetricBCC)
		m.Latencies = map[string][]float64{}
		for _, subnet := range m.Subnets {
			var route detectedRoute
			r.routeMu.Lock()
			if d, ok := r.routeTimes[subnet]; ok {
				route = *d
			}
			r.routeMu.Unlock()
			var pod podMeasurement
			if val, ok := r.podTimes.Load(subnet); ok {
				pod = *val.(*podMeasurement)
			}

			var recorded, missing []string
			for _, ms := range milestones(route, pod) {
				if ms.at.IsZero() {
					missing = append(missing, ms.name)
					continue
				}
				latency := float64(ms.at.Sub(m.Timestamp).Milliseconds())
				m.Latencies[ms.name] = append(m.Latencies[ms.name], latency)
				recorded = append(recorded, fmt.Sprintf("%s=%.0fms", ms.name, latency))
			}
			log.Debugf("BGPRouting %s subnet %s: %s", m.BGPRoutingName, subnet, strings.Join(recorded, " "))
			if len(missing) > 0 {
				log.Warnf("BGPRouting %s subnet %s: no %s recorded", m.BGPRoutingName, subnet, strings.Join(missing, ", "))
			}
		}
		subnets += len(m.Subnets)
		pings += len(m.Latencies["T5"])
		r.NormLatencies = append(r.NormLatencies, m)
		return true
	})
	log.Infof("T5 successful pings: %d/%d", pings, subnets)
	return 0
}

// getLatency reports, per timestamp, the average latency over the BGPRouting's subnets.
// kube-burner aggregates these across BGPRoutings into the quantiles.
func (r *raLatencyBCC) getLatency(normLatency any) map[string]float64 {
	latencies := map[string]float64{}
	for name, samples := range normLatency.(raMetricBCC).Latencies {
		var sum float64
		for _, latency := range samples {
			sum += latency
		}
		latencies[name] = sum / float64(len(samples))
	}
	return latencies
}

func (r *raLatencyBCC) IsCompatible() bool {
	return slices.Contains(supportedRaLatencyBCCJobTypes, r.JobConfig.JobType)
}
