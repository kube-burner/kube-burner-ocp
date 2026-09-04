package workloads

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

func TestNodeDensityFillReplicas(t *testing.T) {
	tests := []struct {
		name        string
		podCapacity int64
		fillPercent int
		currentPods int
		want        int
	}{
		{name: "empty cluster at 80 percent", podCapacity: 250, fillPercent: 80, currentPods: 0, want: 200},
		{name: "subtracts already running pods", podCapacity: 250, fillPercent: 80, currentPods: 50, want: 150},
		{name: "already at or above target", podCapacity: 250, fillPercent: 50, currentPods: 200, want: 0},
		{name: "full capacity", podCapacity: 500, fillPercent: 100, currentPods: 10, want: 490},
		{name: "truncates toward zero", podCapacity: 3, fillPercent: 50, currentPods: 0, want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := nodeDensityFillReplicas(tc.podCapacity, tc.fillPercent, tc.currentPods)
			if got != tc.want {
				t.Fatalf("nodeDensityFillReplicas(%d, %d, %d) = %d, want %d",
					tc.podCapacity, tc.fillPercent, tc.currentPods, got, tc.want)
			}
		})
	}
}

func TestNodeDensityFillLayout(t *testing.T) {
	tests := []struct {
		name                  string
		fillReplicas          int
		maxPodsPerDeployment  int
		wantDeployments       int
		wantPodsPerDeployment int
	}{
		{name: "exact multiple", fillReplicas: 2000, maxPodsPerDeployment: 1000, wantDeployments: 2, wantPodsPerDeployment: 1000},
		{name: "does not round up past fill target", fillReplicas: 2500, maxPodsPerDeployment: 1000, wantDeployments: 3, wantPodsPerDeployment: 833},
		{name: "below cap uses one deployment", fillReplicas: 500, maxPodsPerDeployment: 1000, wantDeployments: 1, wantPodsPerDeployment: 500},
		{name: "just over one cap", fillReplicas: 1001, maxPodsPerDeployment: 1000, wantDeployments: 2, wantPodsPerDeployment: 500},
		{name: "zero fill", fillReplicas: 0, maxPodsPerDeployment: 1000, wantDeployments: 0, wantPodsPerDeployment: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deployments, podsPerDeployment := nodeDensityFillLayout(tc.fillReplicas, tc.maxPodsPerDeployment)
			if deployments != tc.wantDeployments || podsPerDeployment != tc.wantPodsPerDeployment {
				t.Fatalf("nodeDensityFillLayout(%d, %d) = (%d, %d), want (%d, %d)",
					tc.fillReplicas, tc.maxPodsPerDeployment, deployments, podsPerDeployment,
					tc.wantDeployments, tc.wantPodsPerDeployment)
			}
			if deployments*podsPerDeployment > tc.fillReplicas {
				t.Fatalf("layout created %d pods, exceeding fill target %d",
					deployments*podsPerDeployment, tc.fillReplicas)
			}
			if podsPerDeployment > tc.maxPodsPerDeployment {
				t.Fatalf("layout used %d pods per deployment, exceeding cap %d",
					podsPerDeployment, tc.maxPodsPerDeployment)
			}
		})
	}
}

func TestNodeDensityTemplateFillPercentAddsFillJob(t *testing.T) {
	rendered := renderNodeDensityTemplate(t, nodeDensityTemplateData(map[string]any{
		"FILL_PERCENT":        80,
		"DEPLOYMENT_REPLICAS": 3,
		"POD_REPLICAS":        40,
	}))
	if !strings.Contains(rendered, "name: node-density-fill") {
		t.Fatal("expected fill-percent to add the node-density-fill job")
	}
	if !strings.Contains(rendered, "jobIterations: 1") {
		t.Fatal("expected node-density-fill to use a single iteration")
	}
	if !strings.Contains(rendered, "namespacedIterations: false") {
		t.Fatal("expected node-density-fill to disable namespaced iterations")
	}
	if !strings.Contains(rendered, "objectTemplate: deployment.yml") {
		t.Fatal("expected node-density-fill to create a deployment")
	}
	if !strings.Contains(rendered, "replicas: 3") {
		t.Fatal("expected node-density-fill to create multiple deployments")
	}
	if !strings.Contains(rendered, "podReplicas: 40") {
		t.Fatal("expected deployment podReplicas to be rendered")
	}
}

func TestNodeDensityTemplateDefaultUsesPodJob(t *testing.T) {
	rendered := renderNodeDensityTemplate(t, nodeDensityTemplateData(nil))
	if !strings.Contains(rendered, "name: node-density\n") && !strings.Contains(rendered, "name: node-density\r\n") {
		if !strings.Contains(rendered, "- name: node-density") {
			t.Fatal("expected default config to include the node-density job")
		}
	}
	if !strings.Contains(rendered, "objectTemplate: pod.yml") {
		t.Fatal("expected default job to create bare pods")
	}
	if strings.Contains(rendered, "name: node-density-fill") {
		t.Fatal("did not expect node-density-fill when fill-percent is unset")
	}
	if strings.Contains(rendered, "objectTemplate: deployment.yml") {
		t.Fatal("did not expect a deployment when fill-percent is unset")
	}
	if !strings.Contains(rendered, "jobIterations: 42") {
		t.Fatal("expected default job to use JOB_ITERATIONS")
	}
}

func nodeDensityTemplateData(overrides map[string]any) map[string]any {
	data := map[string]any{
		"ALERTS":                   "",
		"BURST":                    5,
		"CHURN_CYCLES":             0,
		"CHURN_DELAY":              "2m0s",
		"CHURN_DURATION":           "0s",
		"CHURN_MODE":               "objects",
		"CHURN_PERCENT":            10,
		"CONTAINER_IMAGE":          "registry.k8s.io/pause:3.1",
		"DELETION_STRATEGY":        "gvr",
		"ES_INDEX":                 "",
		"ES_SERVER":                "",
		"FILL_PERCENT":             0,
		"GC":                       true,
		"GC_METRICS":               false,
		"ITERATIONS_PER_NAMESPACE": 1000,
		"JOB_ITERATIONS":           42,
		"LOCAL_INDEXING":           false,
		"METRICS":                  "metrics.yml",
		"NAMESPACED_ITERATIONS":    true,
		"NODE_SELECTOR":            `{"nodeSelectorTerms":[]}`,
		"POD_READY_THRESHOLD":      0,
		"POD_REPLICAS":             0,
		"DEPLOYMENT_REPLICAS":      0,
		"PPROF":                    false,
		"QPS":                      5,
		"UUID":                     "test-uuid",
	}
	for key, value := range overrides {
		data[key] = value
	}
	return data
}

func renderNodeDensityTemplate(t *testing.T, data map[string]any) string {
	t.Helper()
	templatePath := filepath.Join("..", "..", "cmd", "config", "node-density", "node-density.yml")
	templateBytes, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("failed to read template: %v", err)
	}
	tmpl, err := template.New("node-density").Option("missingkey=error").Parse(string(templateBytes))
	if err != nil {
		t.Fatalf("failed to parse template: %v", err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, data); err != nil {
		t.Fatalf("failed to render template: %v", err)
	}
	return output.String()
}
