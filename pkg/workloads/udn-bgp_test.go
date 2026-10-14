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

package workloads

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/util"
	kubeburnerworkloads "github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

func TestUdnBgpRejectsInvalidCudnsPerRA(t *testing.T) {
	for _, value := range []int{0, -1} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			cmd := NewUdnBgp(&kubeburnerworkloads.WorkloadHelper{}, "udn-bgp")
			if err := cmd.Flags().Set("cudns-per-ra", fmt.Sprint(value)); err != nil {
				t.Fatal(err)
			}
			if err := cmd.PreRunE(cmd, nil); err == nil || !strings.Contains(err.Error(), "--cudns-per-ra must be >= 1") {
				t.Fatalf("expected cudns-per-ra validation error, got %v", err)
			}
		})
	}
}

func TestUdnBgpRASelection(t *testing.T) {
	for _, tc := range []struct {
		name              string
		iterations        int
		namespacesPerCudn int
		cudnsPerRA        int
		wantRAs           int
	}{
		{name: "default", iterations: 72, namespacesPerCudn: 1, wantRAs: 72},
		{name: "grouped", iterations: 72, namespacesPerCudn: 1, cudnsPerRA: 6, wantRAs: 12},
		{name: "remainder", iterations: 14, namespacesPerCudn: 2, cudnsPerRA: 3, wantRAs: 3},
		{name: "single group", iterations: 6, namespacesPerCudn: 2, cudnsPerRA: 10, wantRAs: 1},
	} {
		for _, layer2 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/layer2=%t", tc.name, layer2), func(t *testing.T) {
				cmd := NewUdnBgp(&kubeburnerworkloads.WorkloadHelper{}, "udn-bgp")
				if tc.cudnsPerRA != 0 {
					if err := cmd.Flags().Set("cudns-per-ra", fmt.Sprint(tc.cudnsPerRA)); err != nil {
						t.Fatal(err)
					}
				}
				cudnsPerRA, err := cmd.Flags().GetInt("cudns-per-ra")
				if err != nil {
					t.Fatal(err)
				}
				var spec config.Spec
				renderUdnBgpYAML(t, "udn-bgp.yml", map[string]any{
					"JOB_ITERATIONS": tc.iterations, "NAMESPACES_PER_CUDN": tc.namespacesPerCudn,
					"CUDNS_PER_RA": cudnsPerRA, "CIDRS_PER_CUDN": 2,
					"ENABLE_VM": false, "LAYER2": layer2, "QPS": 20, "BURST": 20,
					"GC": true, "GC_METRICS": false, "ES_SERVER": "", "LOCAL_INDEXING": false,
				}, &spec)
				if len(spec.Jobs) != 4 || spec.Jobs[2].Name != "udn-bgp-create-pods" || spec.Jobs[3].Name != "udn-bgp-route-advertisements" {
					t.Fatalf("expected RA creation job after pods, got %+v", spec.Jobs)
				}
				cudnJob, raJob := spec.Jobs[1], spec.Jobs[3]
				if raJob.JobIterations != tc.wantRAs {
					t.Fatalf("expected %d RAs, got %d", tc.wantRAs, raJob.JobIterations)
				}
				cudns := make([]unstructured.Unstructured, cudnJob.JobIterations)
				for i := range cudns {
					data := map[string]any{"Iteration": i}
					for key, value := range cudnJob.Objects[0].InputVars {
						data[key] = value
					}
					renderUdnBgpYAML(t, "cudn.yml", data, &cudns[i].Object)
				}
				selections := make([]int, len(cudns))
				for i := range raJob.JobIterations {
					var ra unstructured.Unstructured
					renderUdnBgpYAML(t, "ra.yml", map[string]any{"Iteration": i, "layer2": layer2}, &ra.Object)
					selectors, found, err := unstructured.NestedSlice(ra.Object, "spec", "networkSelectors")
					if err != nil || !found || len(selectors) != 1 {
						t.Fatalf("expected one network selector, got %v: %v", selectors, err)
					}
					matchLabels, found, err := unstructured.NestedStringMap(selectors[0].(map[string]any), "clusterUserDefinedNetworkSelector", "networkSelector", "matchLabels")
					if err != nil || !found || len(matchLabels) == 0 {
						t.Fatalf("expected CUDN group labels, got %v: %v", matchLabels, err)
					}
					selector := labels.SelectorFromSet(matchLabels)
					if selector.Matches(labels.Set{fmt.Sprintf("ra-%d", i): ""}) {
						t.Fatalf("RA %s selects CUDNs from cudn-density", ra.GetName())
					}
					selected := 0
					for j, cudn := range cudns {
						if selector.Matches(labels.Set(cudn.GetLabels())) {
							selections[j]++
							selected++
						}
					}
					if selected == 0 || selected > cudnsPerRA {
						t.Fatalf("RA %s selects %d CUDNs, expected 1 to %d", ra.GetName(), selected, cudnsPerRA)
					}
				}
				for i, count := range selections {
					if count != 1 {
						t.Errorf("CUDN %s selected by %d RAs, expected exactly one", cudns[i].GetName(), count)
					}
				}
			})
		}
	}
}

func renderUdnBgpYAML(t *testing.T, name string, data map[string]any, target any) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "cmd", "config", "udn-bgp", name))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := util.RenderTemplate(content, data, util.MissingKeyError, nil)
	if err != nil {
		t.Fatalf("render %s: %v", name, err)
	}
	if err := yaml.Unmarshal(rendered, target); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}
