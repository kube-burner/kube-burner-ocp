package workloads

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	ocpmetadata "github.com/cloud-bulldozer/go-commons/v2/ocp-metadata"
	kubeburnerworkloads "github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"github.com/spf13/cobra"
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

func TestBuildNodeLabelsJSON(t *testing.T) {
	tests := []struct {
		name     string
		selector string
		want     string
	}{
		{
			name:     "default worker selector extracts only equality label",
			selector: "node-role.kubernetes.io/worker=,node-role.kubernetes.io/infra!=,node-role.kubernetes.io/workload!=",
			want:     `{"node-role.kubernetes.io/worker":""}`,
		},
		{
			name:     "equality with value",
			selector: "kubernetes.io/os=linux",
			want:     `{"kubernetes.io/os":"linux"}`,
		},
		{
			name:     "only inequality selectors returns empty",
			selector: "node-role.kubernetes.io/infra!=",
			want:     "",
		},
		{
			name:     "multiple equality labels",
			selector: "node-role.kubernetes.io/worker=,topology.kubernetes.io/zone=us-east-1a",
			want:     "", // will check keys individually since map ordering is not guaranteed
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildNodeLabelsJSON(tt.selector)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.name == "multiple equality labels" {
				if !strings.Contains(got, `"node-role.kubernetes.io/worker":""`) {
					t.Fatalf("expected worker label in output, got %s", got)
				}
				if !strings.Contains(got, `"topology.kubernetes.io/zone":"us-east-1a"`) {
					t.Fatalf("expected zone label in output, got %s", got)
				}
				return
			}
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
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
		"PRELOAD_NODE_LABELS": `{"node-role.kubernetes.io/worker":""}`,
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
