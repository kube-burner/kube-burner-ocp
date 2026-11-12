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
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	pprofComponentOvnkubeController   = "ovnkube-controller"
	pprofComponentOvnkubeControlPlane = "ovnkube-control-plane"
	pprofComponentKubelet             = "kubelet"
	pprofComponentCrio                = "crio"
	ovnKubernetesNamespace            = "openshift-ovn-kubernetes"
	crioSocketPath                    = "/var/run/crio/crio.sock"
)

// pprofTarget is the JSON representation of a kube-burner pprof measurement target
type pprofTarget struct {
	Name           string            `json:"name"`
	Namespace      string            `json:"namespace,omitempty"`
	LabelSelector  map[string]string `json:"labelSelector,omitempty"`
	URL            string            `json:"url"`
	UnixSocketPath string            `json:"unixSocketPath,omitempty"`
}

// pprofComponent describes the pprof targets collected for a component
type pprofComponent struct {
	// nodeLevel components are profiled from the host, through the kube-burner pprof collector DaemonSet
	nodeLevel bool
	targets   []pprofTarget
}

var pprofComponents = map[string]pprofComponent{
	pprofComponentOvnkubeController: {
		targets: []pprofTarget{
			{
				Name:          "ovnkube-controller-heap",
				Namespace:     ovnKubernetesNamespace,
				LabelSelector: map[string]string{"app": "ovnkube-node"},
				URL:           "http://localhost:29103/debug/pprof/heap",
			},
			{
				Name:          "ovnkube-controller-cpu",
				Namespace:     ovnKubernetesNamespace,
				LabelSelector: map[string]string{"app": "ovnkube-node"},
				URL:           "http://localhost:29103/debug/pprof/profile?seconds=60",
			},
		},
	},
	pprofComponentOvnkubeControlPlane: {
		targets: []pprofTarget{
			{
				Name:          "ovnk-control-plane-heap",
				Namespace:     ovnKubernetesNamespace,
				LabelSelector: map[string]string{"app": "ovnkube-control-plane"},
				URL:           "http://localhost:29108/debug/pprof/heap",
			},
			{
				Name:          "ovnk-control-plane-cpu",
				Namespace:     ovnKubernetesNamespace,
				LabelSelector: map[string]string{"app": "ovnkube-control-plane"},
				URL:           "http://localhost:29108/debug/pprof/profile?seconds=60",
			},
		},
	},
	pprofComponentKubelet: {
		nodeLevel: true,
		targets: []pprofTarget{
			{
				Name: "kubelet-heap",
				URL:  "https://${HOSTNAME}:10250/debug/pprof/heap",
			},
			{
				Name: "kubelet-cpu",
				URL:  "https://${HOSTNAME}:10250/debug/pprof/profile?seconds=60",
			},
		},
	},
	pprofComponentCrio: {
		nodeLevel: true,
		targets: []pprofTarget{
			{
				Name:           "crio-heap",
				URL:            "http://localhost/debug/pprof/heap",
				UnixSocketPath: crioSocketPath,
			},
			{
				Name:           "crio-cpu",
				URL:            "http://localhost/debug/pprof/profile?seconds=60",
				UnixSocketPath: crioSocketPath,
			},
		},
	},
}

// defaultPprofComponents are the components profiled when --pprof is enabled
var defaultPprofComponents = []string{pprofComponentOvnkubeController, pprofComponentOvnkubeControlPlane}

// pprofOptions holds the pprof related flags shared by the workloads
type pprofOptions struct {
	enabled    bool
	interval   time.Duration
	components []string
}

// supportedPprofComponents returns the sorted list of components accepted by --pprof-components
func supportedPprofComponents() []string {
	return slices.Sorted(maps.Keys(pprofComponents))
}

// addPprofFlags registers the --pprof, --pprof-interval and --pprof-components flags
func addPprofFlags(cmd *cobra.Command, opts *pprofOptions) {
	cmd.Flags().BoolVar(&opts.enabled, "pprof", false, fmt.Sprintf("Enable pprof collection for the default components: %s", strings.Join(defaultPprofComponents, ",")))
	cmd.Flags().DurationVar(&opts.interval, "pprof-interval", 0, "Interval between pprof collections")
	cmd.Flags().StringSliceVar(&opts.components, "pprof-components", nil, fmt.Sprintf("Comma separated list of components to collect pprof from, it can be combined with --pprof. Supported components: %s", strings.Join(supportedPprofComponents(), ",")))
}

// selectedComponents returns the deduplicated list of components to profile: the default ones when --pprof is enabled, followed by the ones passed with --pprof-components
func (opts pprofOptions) selectedComponents() []string {
	var requested []string
	if opts.enabled {
		requested = append(requested, defaultPprofComponents...)
	}
	requested = append(requested, opts.components...)
	var components []string
	for _, component := range requested {
		component = strings.TrimSpace(component)
		if component == "" || slices.Contains(components, component) {
			continue
		}
		components = append(components, component)
	}
	return components
}

// setPprofVars sets the template variables used to render the pprof measurement of the workloads
func setPprofVars(opts pprofOptions) error {
	components := opts.selectedComponents()
	if err := setPprofTargetVars(components); err != nil {
		return err
	}
	AdditionalVars["PPROF"] = len(components) > 0
	AdditionalVars["PPROF_INTERVAL"] = opts.interval.String()
	return nil
}

// setPprofTargetVars sets PPROF_TARGETS, the JSON list of pprof targets of the given components, and PPROF_NODE_TARGETS,
// which reports whether any of them must be collected from the nodes. The default components are used when the list is empty,
// so configurations enabling pprof through the PPROF variable keep collecting the default profiles
func setPprofTargetVars(components []string) error {
	if len(components) == 0 {
		components = defaultPprofComponents
	}
	targets := []pprofTarget{}
	var nodeTargets bool
	for _, name := range components {
		component, ok := pprofComponents[name]
		if !ok {
			return fmt.Errorf("unsupported pprof component %q, supported components: %s", name, strings.Join(supportedPprofComponents(), ","))
		}
		targets = append(targets, component.targets...)
		nodeTargets = nodeTargets || component.nodeLevel
	}
	targetsJSON, err := json.Marshal(targets)
	if err != nil {
		return fmt.Errorf("error marshaling pprof targets: %w", err)
	}
	AdditionalVars["PPROF_TARGETS"] = string(targetsJSON)
	AdditionalVars["PPROF_NODE_TARGETS"] = nodeTargets
	return nil
}
