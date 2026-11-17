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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/measurements/types"
	kubeburnerutil "github.com/kube-burner/kube-burner/v2/pkg/util"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// pprofWorkloadConfigs are the embedded workload configurations whose pprof targets are rendered from --pprof and --pprof-components
var pprofWorkloadConfigs = []string{
	"cluster-density-v2/cluster-density-v2.yml",
	"cudn-density/cudn-density.yml",
	"node-density/node-density.yml",
	"node-density-cni/node-density-cni.yml",
	"node-density-heavy/node-density-heavy.yml",
	"udn-bgp/udn-bgp.yml",
	"udn-density-pods/udn-density-pods.yml",
	"udn-density-pods/cudn-density-pods.yml",
	"virt-udn-density/virt-udn-density.yml",
	"virt-udn-density/virt-cudn-density.yml",
}

func resetAdditionalVars(t *testing.T) {
	t.Helper()
	restoreAdditionalVars := AdditionalVars
	AdditionalVars = map[string]any{}
	t.Cleanup(func() {
		AdditionalVars = restoreAdditionalVars
	})
}

func TestPprofSelectedComponents(t *testing.T) {
	tests := []struct {
		name     string
		opts     pprofOptions
		expected []string
	}{
		{
			name: "disabled",
			opts: pprofOptions{},
		},
		{
			name:     "pprof flag selects the default components",
			opts:     pprofOptions{enabled: true},
			expected: []string{pprofComponentOvnkubeController, pprofComponentOvnkubeControlPlane},
		},
		{
			name:     "explicit components",
			opts:     pprofOptions{components: []string{pprofComponentCrio, pprofComponentKubelet}},
			expected: []string{pprofComponentCrio, pprofComponentKubelet},
		},
		{
			name:     "pprof flag combined with explicit components",
			opts:     pprofOptions{enabled: true, components: []string{pprofComponentKubelet, pprofComponentOvnkubeController}},
			expected: []string{pprofComponentOvnkubeController, pprofComponentOvnkubeControlPlane, pprofComponentKubelet},
		},
		{
			name:     "duplicated and empty components are ignored",
			opts:     pprofOptions{components: []string{" crio", "", "crio ", "kubelet"}},
			expected: []string{pprofComponentCrio, pprofComponentKubelet},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.opts.selectedComponents(); !slices.Equal(got, tt.expected) {
				t.Fatalf("expected components %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestSetPprofVarsRejectsUnsupportedComponent(t *testing.T) {
	resetAdditionalVars(t)
	err := setPprofVars(pprofOptions{components: []string{pprofComponentKubelet, "etcd"}})
	if err == nil {
		t.Fatal("expected an error for an unsupported pprof component")
	}
	if !strings.Contains(err.Error(), `"etcd"`) || !strings.Contains(err.Error(), strings.Join(supportedPprofComponents(), ",")) {
		t.Fatalf("expected error to report the unsupported and supported components, got: %v", err)
	}
}

func TestPprofFlags(t *testing.T) {
	var opts pprofOptions
	cmd := &cobra.Command{Use: "test"}
	addPprofFlags(cmd, &opts)
	if err := cmd.ParseFlags([]string{"--pprof", "--pprof-interval=5m", "--pprof-components=kubelet,crio"}); err != nil {
		t.Fatalf("failed to parse flags: %v", err)
	}
	if !opts.enabled || opts.interval != 5*time.Minute || !slices.Equal(opts.components, []string{pprofComponentKubelet, pprofComponentCrio}) {
		t.Fatalf("unexpected pprof options: %+v", opts)
	}
}

func TestPprofWorkloadConfigs(t *testing.T) {
	tests := []struct {
		name                string
		opts                pprofOptions
		expectedTargets     []string
		expectedNodeTargets bool
	}{
		{
			name: "disabled",
			opts: pprofOptions{},
		},
		{
			name:            "default components",
			opts:            pprofOptions{enabled: true, interval: 5 * time.Minute},
			expectedTargets: []string{"ovnkube-controller-heap", "ovnkube-controller-cpu", "ovnk-control-plane-heap", "ovnk-control-plane-cpu"},
		},
		{
			name:                "node level components",
			opts:                pprofOptions{components: []string{pprofComponentOvnkubeController, pprofComponentKubelet, pprofComponentCrio}},
			expectedTargets:     []string{"ovnkube-controller-heap", "ovnkube-controller-cpu", "kubelet-heap", "kubelet-cpu", "crio-heap", "crio-cpu"},
			expectedNodeTargets: true,
		},
	}
	for _, configFile := range pprofWorkloadConfigs {
		for _, tt := range tests {
			t.Run(configFile+"/"+tt.name, func(t *testing.T) {
				resetAdditionalVars(t)
				if err := setPprofVars(tt.opts); err != nil {
					t.Fatalf("failed to set pprof variables: %v", err)
				}
				measurement := renderPprofMeasurement(t, filepath.Join("..", "..", "cmd", "config", configFile))
				if tt.expectedTargets == nil {
					if measurement != nil {
						t.Fatal("did not expect a pprof measurement")
					}
					return
				}
				if measurement == nil {
					t.Fatal("expected a pprof measurement")
				}
				if measurement.PProfInterval != tt.opts.interval {
					t.Fatalf("expected pprof interval %v, got %v", tt.opts.interval, measurement.PProfInterval)
				}
				var targetNames []string
				for _, target := range measurement.PProfTargets {
					targetNames = append(targetNames, target.Name)
					if target.URL == "" {
						t.Fatalf("pprof target %s has no URL", target.Name)
					}
				}
				if !slices.Equal(targetNames, tt.expectedTargets) {
					t.Fatalf("expected pprof targets %v, got %v", tt.expectedTargets, targetNames)
				}
				workerAffinity := map[string]string{"node-role.kubernetes.io/worker": ""}
				if tt.expectedNodeTargets != maps.Equal(measurement.NodeAffinity, workerAffinity) {
					t.Fatalf("unexpected pprof nodeAffinity %v", measurement.NodeAffinity)
				}
			})
		}
	}
}

func TestPprofTargetsMatchMeasurementSchema(t *testing.T) {
	resetAdditionalVars(t)
	if err := setPprofTargetVars(supportedPprofComponents()); err != nil {
		t.Fatalf("failed to set pprof target variables: %v", err)
	}
	var targets []types.PProftarget
	if err := yaml.Unmarshal([]byte(AdditionalVars["PPROF_TARGETS"].(string)), &targets); err != nil {
		t.Fatalf("failed to decode pprof targets: %v", err)
	}
	expected := map[string]types.PProftarget{
		"ovnkube-controller-cpu": {
			Name:          "ovnkube-controller-cpu",
			Namespace:     "openshift-ovn-kubernetes",
			LabelSelector: map[string]string{"app": "ovnkube-node"},
			URL:           "http://localhost:29103/debug/pprof/profile?seconds=60",
		},
		"kubelet-heap": {
			Name: "kubelet-heap",
			URL:  "https://${HOSTNAME}:10250/debug/pprof/heap",
		},
		"crio-cpu": {
			Name:           "crio-cpu",
			URL:            "http://localhost/debug/pprof/profile?seconds=60",
			UnixSocketPath: "/var/run/crio/crio.sock",
		},
	}
	var found int
	for _, target := range targets {
		want, ok := expected[target.Name]
		if !ok {
			continue
		}
		found++
		if target.Namespace != want.Namespace || target.URL != want.URL || target.UnixSocketPath != want.UnixSocketPath || !maps.Equal(target.LabelSelector, want.LabelSelector) {
			t.Fatalf("expected pprof target %+v, got %+v", want, target)
		}
	}
	if found != len(expected) {
		t.Fatalf("expected to find %d pprof targets, found %d in %v", len(expected), found, targets)
	}
}

// renderPprofMeasurement renders a workload configuration as kube-burner does and returns its pprof measurement, if any
func renderPprofMeasurement(t *testing.T, configFile string) *types.Measurement {
	t.Helper()
	// Variables that cannot take the empty defaults set by defaultUndefinedTemplateVars
	AdditionalVars["ENABLE_LAYER_3"] = false
	AdditionalVars["JOB_ITERATIONS"] = 1
	AdditionalVars["NAMESPACES_PER_CUDN"] = 1
	defaultUndefinedTemplateVars(configFile)
	configBytes, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("failed to read %s: %v", configFile, err)
	}
	inputData := kubeburnerutil.EnvToMap()
	maps.Copy(inputData, AdditionalVars)
	rendered, err := kubeburnerutil.RenderTemplate(configBytes, inputData, kubeburnerutil.MissingKeyError, nil)
	if err != nil {
		t.Fatalf("failed to render %s: %v", configFile, err)
	}
	var config struct {
		Global struct {
			Measurements []types.Measurement `yaml:"measurements"`
		} `yaml:"global"`
	}
	if err := yaml.Unmarshal(rendered, &config); err != nil {
		t.Fatalf("failed to decode rendered %s: %v", configFile, err)
	}
	for i, measurement := range config.Global.Measurements {
		if measurement.Name == "pprof" {
			return &config.Global.Measurements[i]
		}
	}
	return nil
}
