package workloads

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	ocpmetadata "github.com/cloud-bulldozer/go-commons/v2/ocp-metadata"
	kubeburnerworkloads "github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"github.com/spf13/cobra"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

func TestApplyClusterInfoPopulatesMetadataAndCapabilities(t *testing.T) {
	restoreClusterMetadata := clusterMetadata
	restoreClusterCapabilities := clusterCapabilities
	t.Cleanup(func() {
		clusterMetadata = restoreClusterMetadata
		clusterCapabilities = restoreClusterCapabilities
	})
	wh := &kubeburnerworkloads.WorkloadHelper{}
	clusterInfo := ocpmetadata.ClusterInfo{
		Metadata: ocpmetadata.ClusterMetadata{
			Distribution:           ocpmetadata.DistributionMicroShift,
			MicroShift:             true,
			MicroShiftVersion:      "4.22.0~rc.2",
			MicroShiftMajorVersion: "4.22",
			K8SVersion:             "v1.35.3",
			TotalNodes:             1,
		},
		Capabilities: ocpmetadata.ClusterCapabilities{
			APIGroups: map[string]bool{
				"image.openshift.io":               true,
				ocpmetadata.APIGroupOpenShiftRoute: true,
			},
		},
	}

	if err := applyClusterInfo(wh, clusterInfo); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wh.SummaryMetadata["distribution"] != ocpmetadata.DistributionMicroShift {
		t.Fatalf("expected summary distribution microshift, got %v", wh.SummaryMetadata["distribution"])
	}
	if wh.SummaryMetadata["microshift"] != true {
		t.Fatalf("expected summary microshift=true, got %v", wh.SummaryMetadata["microshift"])
	}
	if wh.SummaryMetadata["totalNodes"] != float64(1) {
		t.Fatalf("expected summary totalNodes=1, got %v", wh.SummaryMetadata["totalNodes"])
	}
	for key, want := range map[string]any{
		"distribution":           ocpmetadata.DistributionMicroShift,
		"microshift":             true,
		"microshiftVersion":      "4.22.0~rc.2",
		"microshiftMajorVersion": "4.22",
		"k8sVersion":             "v1.35.3",
		"totalNodes":             1,
	} {
		if wh.MetricsMetadata[key] != want {
			t.Fatalf("expected metrics %s=%v, got %v", key, want, wh.MetricsMetadata[key])
		}
	}
	if !HasAPIGroup("image.openshift.io") {
		t.Fatal("expected ImageStream capability to be true")
	}
	if !HasAPIGroup(ocpmetadata.APIGroupOpenShiftRoute) {
		t.Fatal("expected Route capability to be true")
	}
	if HasAPIGroup(ocpmetadata.APIGroupOpenShiftBuild) {
		t.Fatal("did not expect Build capability")
	}
	if !IsMicroShift() {
		t.Fatal("expected IsMicroShift to return true")
	}
}

func TestClusterDensityNeedsIngressDomain(t *testing.T) {
	if !clusterDensityNeedsIngressDomain("cluster-density-v2") {
		t.Fatal("expected cluster-density-v2 to require ingress domain")
	}
	if clusterDensityNeedsIngressDomain("cluster-density-ms") {
		t.Fatal("did not expect cluster-density-ms to require ingress domain")
	}
}

func TestMetricsMetadataOmitsAbsentOptionalValues(t *testing.T) {
	metadata := metricsMetadataFromClusterMetadata(ocpmetadata.ClusterMetadata{
		Distribution: ocpmetadata.DistributionOpenShift,
		MicroShift:   false,
	})

	if metadata["distribution"] != ocpmetadata.DistributionOpenShift {
		t.Fatalf("expected distribution openshift, got %v", metadata["distribution"])
	}
	if metadata["microshift"] != false {
		t.Fatalf("expected microshift=false, got %v", metadata["microshift"])
	}
	for _, key := range []string{"microshiftVersion", "microshiftMajorVersion", "k8sVersion", "totalNodes", "ocpVersion", "ocpMajorVersion"} {
		if _, ok := metadata[key]; ok {
			t.Fatalf("did not expect absent metadata key %q", key)
		}
	}
}

func TestSetMetricsDefaultsToRegularProfileOnMicroShift(t *testing.T) {
	t.Setenv("METRICS", "")
	restoreClusterMetadata := clusterMetadata
	t.Cleanup(func() {
		clusterMetadata = restoreClusterMetadata
	})
	clusterMetadata = ocpmetadata.ClusterMetadata{MicroShift: true}

	setMetrics(testMetricsProfileCmd(t, false), []string{"microshift-metrics.yml"})

	if got := os.Getenv("METRICS"); got != "microshift-metrics.yml" {
		t.Fatalf("expected MicroShift default metrics profile only, got %q", got)
	}
}

func TestSetMetricsHonorsExplicitBothProfileOnMicroShift(t *testing.T) {
	t.Setenv("METRICS", "")
	restoreClusterMetadata := clusterMetadata
	t.Cleanup(func() {
		clusterMetadata = restoreClusterMetadata
	})
	clusterMetadata = ocpmetadata.ClusterMetadata{MicroShift: true}

	setMetrics(testMetricsProfileCmd(t, true), []string{"microshift-metrics.yml"})

	if got := os.Getenv("METRICS"); got != "microshift-metrics.yml,metrics-report.yml" {
		t.Fatalf("expected explicit both profile to include reporting metrics, got %q", got)
	}
}

func TestClusterDensityMSTemplateGatesOptionalOpenShiftObjects(t *testing.T) {
	withoutAPIs := renderClusterDensityMSTemplate(t, clusterDensityMSTemplateData(map[string]any{
		"HAS_IMAGESTREAM_API": false,
		"HAS_ROUTE_API":       false,
	}))
	if strings.Contains(withoutAPIs, "imagestream.yml") {
		t.Fatal("did not expect imagestream.yml without ImageStream API")
	}
	if strings.Contains(withoutAPIs, "route.yml") {
		t.Fatal("did not expect route.yml without Route API")
	}

	withAPIs := renderClusterDensityMSTemplate(t, clusterDensityMSTemplateData(map[string]any{
		"HAS_IMAGESTREAM_API": true,
		"HAS_ROUTE_API":       true,
	}))
	if !strings.Contains(withAPIs, "imagestream.yml") {
		t.Fatal("expected imagestream.yml with ImageStream API")
	}
	if !strings.Contains(withAPIs, "route.yml") {
		t.Fatal("expected route.yml with Route API")
	}
}

func TestClusterDensityMSTemplateRendersWithMetricsEndpointMode(t *testing.T) {
	rendered := renderClusterDensityMSTemplate(t, clusterDensityMSTemplateData(map[string]any{
		"HAS_IMAGESTREAM_API": false,
		"HAS_ROUTE_API":       true,
	}))
	if strings.Contains(rendered, "type: opensearch") {
		t.Fatal("did not expect embedded OpenSearch indexer when ES_SERVER is empty")
	}
}

func clusterDensityMSTemplateData(overrides map[string]any) map[string]any {
	data := map[string]any{
		"ALERTS":              "",
		"BURST":               5,
		"CHURN_CYCLES":        0,
		"CHURN_DELAY":         "2m0s",
		"CHURN_DURATION":      "0s",
		"CHURN_MODE":          "namespaces",
		"CHURN_PERCENT":       10,
		"DELETION_STRATEGY":   "default",
		"ES_INDEX":            "",
		"ES_SERVER":           "",
		"GC":                  true,
		"GC_METRICS":          false,
		"HAS_IMAGESTREAM_API": false,
		"HAS_ROUTE_API":       false,
		"JOB_ITERATIONS":      1,
		"LOCAL_INDEXING":      false,
		"METRICS":             "microshift-metrics.yml",
		"NODE_SELECTOR":       `{"nodeSelectorTerms":[{"matchExpressions":[{"key":"node-role.kubernetes.io/worker","operator":"Exists"}]}]}`,
		"POD_READY_THRESHOLD": 0,
		"PPROF":               false,
		"QPS":                 5,
		"SVC_LATENCY":         false,
		"UUID":                "test-uuid",
	}
	for key, value := range overrides {
		data[key] = value
	}
	return data
}

func testMetricsProfileCmd(t *testing.T, explicitProfileType bool) *cobra.Command {
	t.Helper()
	root := &cobra.Command{Use: "root"}
	root.PersistentFlags().String("profile-type", "both", "")
	if explicitProfileType {
		if err := root.PersistentFlags().Set("profile-type", "both"); err != nil {
			t.Fatalf("failed to set profile-type: %v", err)
		}
	}
	cmd := &cobra.Command{Use: "cluster-density-ms"}
	root.AddCommand(cmd)
	return cmd
}

func renderClusterDensityMSTemplate(t *testing.T, data map[string]any) string {
	t.Helper()
	templatePath := filepath.Join("..", "..", "cmd", "config", "cluster-density-ms", "cluster-density-ms.yml")
	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("failed to read template: %v", err)
	}
	tmpl, err := template.New("cluster-density-ms").Option("missingkey=error").Parse(string(templateBytes))
	if err != nil {
		t.Fatalf("failed to parse template: %v", err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		t.Fatalf("failed to render template: %v", err)
	}
	return output.String()
}

type testK8SConnector struct {
	clientSet     kubernetes.Interface
	dynamicClient dynamic.Interface
}

func (c *testK8SConnector) ClientSet() kubernetes.Interface {
	return c.clientSet
}

func (c *testK8SConnector) RestConfig() *rest.Config {
	return nil
}

func (c *testK8SConnector) DynamicClient() dynamic.Interface {
	return c.dynamicClient
}

func TestGetVolumeSnapshotClassName(t *testing.T) {
	const (
		storageClassName = "sc-array-2"
		provisioner      = "csi.example.com"
	)
	tests := []struct {
		name    string
		objects []runtime.Object
		want    string
	}{
		{
			name: "uses the snapshotClass of the StorageProfile when several classes share the provisioner",
			objects: []runtime.Object{
				testVolumeSnapshotClass("vol-snap-array-1", provisioner),
				testVolumeSnapshotClass("vol-snap-array-2", provisioner),
				testStorageProfile(storageClassName, map[string]any{"snapshotClass": "vol-snap-array-2"}),
			},
			want: "vol-snap-array-2",
		},
		{
			name: "falls back to the provisioner match when the StorageProfile has no snapshotClass",
			objects: []runtime.Object{
				testVolumeSnapshotClass("vol-snap-other", "csi.other.com"),
				testVolumeSnapshotClass("vol-snap-array-2", provisioner),
				testStorageProfile(storageClassName, map[string]any{}),
			},
			want: "vol-snap-array-2",
		},
		{
			name: "falls back to the provisioner match when there is no StorageProfile",
			objects: []runtime.Object{
				testVolumeSnapshotClass("vol-snap-other", "csi.other.com"),
				testVolumeSnapshotClass("vol-snap-array-2", provisioner),
			},
			want: "vol-snap-array-2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connector := newSnapshotClassTestConnector(storageClassName, provisioner, tt.objects...)

			got, err := getVolumeSnapshotClassName(connector, storageClassName)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected VolumeSnapshotClass %q, got %q", tt.want, got)
			}
		})
	}
}

func TestGetVolumeSnapshotClassNameStorageProfileError(t *testing.T) {
	connector := newSnapshotClassTestConnector("sc", "csi.example.com", testVolumeSnapshotClass("vol-snap", "csi.example.com"))
	connector.dynamicClient.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "storageprofiles", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})

	if _, err := getVolumeSnapshotClassName(connector, "sc"); err == nil {
		t.Fatal("expected an error when the StorageProfile cannot be retrieved")
	}
}

func newSnapshotClassTestConnector(storageClassName, provisioner string, objects ...runtime.Object) *testK8SConnector {
	return &testK8SConnector{
		clientSet: kubefake.NewClientset(&storagev1.StorageClass{
			ObjectMeta:  metav1.ObjectMeta{Name: storageClassName},
			Provisioner: provisioner,
		}),
		dynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			runtime.NewScheme(),
			map[schema.GroupVersionResource]string{
				{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotclasses"}: "VolumeSnapshotClassList",
				{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "storageprofiles"}:          "StorageProfileList",
			},
			objects...,
		),
	}
}

func testVolumeSnapshotClass(name, driver string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion":     "snapshot.storage.k8s.io/v1",
		"kind":           "VolumeSnapshotClass",
		"metadata":       map[string]any{"name": name},
		"driver":         driver,
		"deletionPolicy": "Delete",
	}}
}

func testStorageProfile(name string, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cdi.kubevirt.io/v1beta1",
		"kind":       "StorageProfile",
		"metadata":   map[string]any{"name": name},
		"status":     status,
	}}
}
