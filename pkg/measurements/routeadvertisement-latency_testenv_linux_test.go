//go:build integration

// Copyright 2026 The Kube-burner Authors.
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
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	kbmeasurements "github.com/kube-burner/kube-burner/v2/pkg/measurements"
	rav1 "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/routeadvertisements/v1"
	raclient "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/routeadvertisements/v1/apis/clientset/versioned"
	ovntypes "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/types"
	udnv1 "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/userdefinednetwork/v1"
	udnclient "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/userdefinednetwork/v1/apis/clientset/versioned"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const (
	raIntegrationChild = "KUBE_BURNER_RA_TEST_NETNS"
	raTargetIP         = "10.200.0.2"
	raTargetSubnet     = "10.200.0.0/24"
	raImportTargets    = 12 // More than the ten setup workers, to exercise concurrent pingers.

	// Track the latest CRDs on the OpenShift repository's default branch.
	raIntegrationCRDBranch    = "main"
	raIntegrationCRDDirectory = "KUBE_BURNER_RA_TEST_CRD_DIRECTORY"
)

// Re-exec before the Go runtime starts so every worker thread and socket belongs
// to the test namespace. LockOSThread around Start alone cannot isolate workers.
func runRAIntegrationChild(t *testing.T) {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Fatal("KUBEBUILDER_ASSETS is required; run make test-ra-integration")
	}
	// Download before isolation: the child namespace has no external network.
	crdDirectory := downloadRAIntegrationCRDs(t)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run="+flag.Lookup("test.run").Value.String(), "-test.v", "-test.timeout=4m")
	cmd.Env = append(os.Environ(), raIntegrationChild+"=1", raIntegrationCRDDirectory+"="+crdDirectory)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET, Pdeathsig: syscall.SIGKILL}
	if os.Geteuid() != 0 {
		cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}}
		cmd.SysProcAttr.GidMappingsEnableSetgroups = false
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start isolated test process: %v; namespace creation requires root or enabled user namespaces (see testdata/routeadvertisement-latency/README.md)", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("isolated integration suite failed: %v", err)
	}
}

// downloadRAIntegrationCRDs keeps manifests outside the checkout for this run.
// The parent test owns the directory until the isolated child has stopped.
func downloadRAIntegrationCRDs(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	client := &http.Client{Timeout: 30 * time.Second}
	const maxCRDBytes = 2 << 20
	for _, filename := range []string{
		"k8s.ovn.org_routeadvertisements.yaml",
		"k8s.ovn.org_clusteruserdefinednetworks.yaml",
	} {
		url := fmt.Sprintf("https://raw.githubusercontent.com/openshift/ovn-kubernetes/%s/helm/ovn-kubernetes/crds/%s", raIntegrationCRDBranch, filename)
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("download CRD %s: %v", url, err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("download CRD %s: %s", url, response.Status)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, maxCRDBytes+1))
		if err != nil {
			t.Fatalf("read CRD %s: %v", url, err)
		}
		if len(data) > maxCRDBytes {
			t.Fatalf("CRD %s exceeds %d bytes", url, maxCRDBytes)
		}
		if err := os.WriteFile(filepath.Join(directory, filename), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

type raIntegrationAPI struct {
	config *rest.Config
	core   kubernetes.Interface
	ra     raclient.Interface
	udn    udnclient.Interface
}

func startRAIntegrationAPI(t *testing.T) *raIntegrationAPI {
	t.Helper()
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		t.Fatal(err)
	}
	// Export uses unprivileged ICMP echo sockets; import uses raw ICMP sockets.
	if err := os.WriteFile("/proc/sys/net/ipv4/ping_group_range", []byte("0 0"), 0600); err != nil {
		t.Fatal(err)
	}
	crdDirectory := os.Getenv(raIntegrationCRDDirectory)
	if crdDirectory == "" {
		t.Fatalf("%s is missing; start tests through the parent process", raIntegrationCRDDirectory)
	}
	useExistingCluster := false
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{crdDirectory},
		ErrorIfCRDPathMissing: true,
		UseExistingCluster:    &useExistingCluster,
	}
	// The isolated namespace deliberately has no default route or external NIC.
	testEnv.ControlPlane.GetAPIServer().Configure().Set("advertise-address", "127.0.0.1")
	t.Cleanup(func() {
		if err := testEnv.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	core, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ra, err := raclient.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	udn, err := udnclient.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &raIntegrationAPI{config: cfg, core: core, ra: ra, udn: udn}
}

type raIntegrationNetwork struct {
	peer netns.NsHandle
	link netlink.Link
}

func newRAIntegrationNetwork(t *testing.T) *raIntegrationNetwork {
	t.Helper()
	peer := newRATargetNamespace(t)
	t.Cleanup(func() { peer.Close() })
	handle, err := netlink.NewHandleAt(peer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handle.Close)
	veth := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "ra-test"}, PeerName: "ra-peer", PeerNamespace: netlink.NsFd(peer)}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := netlink.LinkDel(veth); err != nil {
			t.Errorf("delete test veth: %v", err)
		}
	})
	peerLink, err := handle.LinkByName("ra-peer")
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []func() error{
		func() error { return netlink.AddrAdd(veth, raTestAddr(t, "192.0.2.1/30")) },
		func() error { return netlink.LinkSetUp(veth) },
		func() error { return handle.AddrAdd(peerLink, raTestAddr(t, "192.0.2.2/30")) },
		func() error { return handle.LinkSetUp(peerLink) },
	} {
		if err := operation(); err != nil {
			t.Fatal(err)
		}
	}
	lo, err := handle.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.LinkSetUp(lo); err != nil {
		t.Fatal(err)
	}
	if err := handle.AddrAdd(lo, raTestAddr(t, raTargetIP+"/32")); err != nil {
		t.Fatal(err)
	}
	// Reply to source addresses assigned by import workers on dummy interfaces.
	if err := handle.RouteAdd(&netlink.Route{Dst: raTestCIDR(t, "20.0.0.0/8"), Gw: net.ParseIP("192.0.2.1"), LinkIndex: peerLink.Attrs().Index}); err != nil {
		t.Fatal(err)
	}
	network := &raIntegrationNetwork{peer: peer, link: veth}
	network.answerPings(t, false)
	return network
}

func newRATargetNamespace(t *testing.T) netns.NsHandle {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	original, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	peer, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := netns.Set(original); err != nil {
		peer.Close()
		t.Fatal(err)
	}
	return peer
}

func inRATargetNamespace(t *testing.T, peer netns.NsHandle, operation func() error) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	original, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if err := netns.Set(peer); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := netns.Set(original); err != nil {
			t.Fatalf("restore measurement namespace: %v", err)
		}
	}()
	if err := operation(); err != nil {
		t.Fatal(err)
	}
}

func (n *raIntegrationNetwork) answerPings(t *testing.T, enabled bool) {
	t.Helper()
	value := "1"
	if enabled {
		value = "0"
	}
	inRATargetNamespace(t, n.peer, func() error {
		return os.WriteFile("/proc/sys/net/ipv4/icmp_echo_ignore_all", []byte(value), 0600)
	})
}

func (n *raIntegrationNetwork) addTargetRoute(t *testing.T) {
	t.Helper()
	if err := netlink.RouteAdd(&netlink.Route{Dst: raTestCIDR(t, raTargetSubnet), Gw: net.ParseIP("192.0.2.2"), LinkIndex: n.link.Attrs().Index}); err != nil {
		t.Fatal(err)
	}
}

func (n *raIntegrationNetwork) echoRequests(t *testing.T) uint64 {
	t.Helper()
	var requests uint64
	inRATargetNamespace(t, n.peer, func() error {
		// /proc/net follows the process leader, which remains in the measurement
		// namespace. thread-self follows this locked thread into the target.
		data, err := os.ReadFile("/proc/thread-self/net/snmp")
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if !strings.HasPrefix(line, "Icmp:") || i+1 >= len(lines) {
				continue
			}
			names, values := strings.Fields(line), strings.Fields(lines[i+1])
			for column, name := range names {
				if name == "InEchos" && column < len(values) {
					requests, err = strconv.ParseUint(values[column], 10, 64)
					return err
				}
			}
		}
		return fmt.Errorf("Icmp InEchos is missing from /proc/thread-self/net/snmp")
	})
	return requests
}

func raTestAddr(t *testing.T, cidr string) *netlink.Addr {
	t.Helper()
	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func raTestCIDR(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, subnet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return subnet
}

func seedRAIntegrationTargets(t *testing.T, api *raIntegrationAPI, name string) {
	t.Helper()
	ctx := t.Context()
	labels := map[string]string{config.KubeBurnerLabelUUID: name}
	if _, err := api.core.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	cudn := &udnv1.ClusterUserDefinedNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"ra-test": name}},
		Spec: udnv1.ClusterUserDefinedNetworkSpec{
			NamespaceSelector: metav1.LabelSelector{MatchLabels: labels},
			Network: udnv1.NetworkSpec{Topology: udnv1.NetworkTopologyLayer3, Layer3: &udnv1.Layer3Config{
				Role: udnv1.NetworkRolePrimary, Subnets: []udnv1.Layer3Subnet{{CIDR: udnv1.CIDR(raTargetSubnet), HostSubnet: 28}},
			}},
		},
	}
	if _, err := api.udn.K8sV1().ClusterUserDefinedNetworks().Create(ctx, cudn, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: name, Labels: labels, Annotations: map[string]string{
			"k8s.ovn.org/pod-networks": fmt.Sprintf(`{"%s/%s":{"ip_address":"%s/24","mac_address":"0a:58:0a:c8:00:02","role":"primary"}}`, name, name, raTargetIP),
		}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "target", Image: "test.invalid/unused:latest"}}},
	}
	if _, err := api.core.CoreV1().Pods(name).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func createAcceptedRA(t *testing.T, api *raIntegrationAPI, r *raLatency, name string) {
	t.Helper()
	ra := &rav1.RouteAdvertisements{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: rav1.RouteAdvertisementsSpec{
			NetworkSelectors: ovntypes.NetworkSelectors{{
				NetworkSelectionType: ovntypes.ClusterUserDefinedNetworks,
				ClusterUserDefinedNetworkSelector: &ovntypes.ClusterUserDefinedNetworkSelector{
					NetworkSelector: metav1.LabelSelector{MatchLabels: map[string]string{"ra-test": name}},
				},
			}},
			Advertisements: []rav1.AdvertisementType{rav1.PodNetwork},
		},
	}
	created, err := api.ra.K8sV1().RouteAdvertisements().Create(t.Context(), ra, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := api.ra.K8sV1().RouteAdvertisements().Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			t.Errorf("delete RA fixture: %v", err)
		}
	})
	raEventually(t, "RA add event", func() bool { _, ok := r.GetMetrics().Load(name); return ok })
	created.Status.Conditions = []metav1.Condition{{
		Type: raAcceptedConditionType, Status: metav1.ConditionTrue, Reason: "TestAccepted",
		Message: "accepted by the integration harness", LastTransitionTime: metav1.Now(), ObservedGeneration: created.Generation,
	}}
	if _, err := api.ra.K8sV1().RouteAdvertisements().UpdateStatus(t.Context(), created, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	raEventually(t, "RA accepted event", func() bool { return r.observer.acceptedCount() == 1 })
}

func startRAIntegrationMeasurement(t *testing.T, api *raIntegrationAPI, name, mode string, timeout time.Duration) (*raLatency, func() <-chan error) {
	t.Helper()
	oldIfaces, oldAddresses, oldCount := numDummyIfaces, numAddressOnDummyIface, importRoutesCount
	oldExportTimeout, oldImportTimeout := exportScenarioMaxTimeout, importScenarioMaxTimeout
	t.Cleanup(func() {
		numDummyIfaces, numAddressOnDummyIface, importRoutesCount = oldIfaces, oldAddresses, oldCount
		exportScenarioMaxTimeout, importScenarioMaxTimeout = oldExportTimeout, oldImportTimeout
	})
	r := &raLatency{
		BaseMeasurement: kbmeasurements.BaseMeasurement{
			Uuid: name, ClientSet: api.core, RestConfig: api.config,
			MeasurementName: raLatencyMeasurement, QuantilesMeasurementName: raLatencyQuantilesMeasurement,
			JobConfig: &config.Job{Name: name, JobType: config.CreationJob, JobIterations: 1, Objects: []config.Object{{InputVars: map[string]any{
				"raConnectionCheck": mode, "numDummyIfaces": 1, "numAddressOnDummyIface": raImportTargets,
				"exportScenarioMaxTimeout": timeout.String(), "importScenarioMaxTimeout": timeout.String(),
			}}}},
		},
		scenarioPollInterval: 20 * time.Millisecond,
	}
	var wg sync.WaitGroup
	wg.Add(1)
	if err := r.Start(&wg); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	wg.Add(1)
	r.Collect(&wg)
	wg.Wait()
	var once sync.Once
	stopped := make(chan error, 1)
	stop := func() <-chan error {
		once.Do(func() { go func() { stopped <- r.Stop(); close(stopped) }() })
		return stopped
	}
	t.Cleanup(func() {
		select {
		case err := <-stop():
			if err != nil {
				t.Logf("Stop during cleanup: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("measurement cleanup did not join workers")
		}
	})
	return r, stop
}

func raEventually(t *testing.T, description string, ready func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", description)
		case <-ticker.C:
		}
	}
}
