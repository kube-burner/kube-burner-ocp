// Copyright 2022 The Kube-burner Authors.
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

package ocp

import (
	"fmt"
	"net/netip"
	"os"
	"time"

	"github.com/kube-burner/kube-burner/pkg/util"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	"github.com/spf13/cobra"
)

// NewNetworkPolicy holds network-policy workload
func NewNetworkPolicy(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, podsPerNamespace, netpolPerNamespace, localPods, podSelectors, singlePorts, portRanges, remoteNamespaces, remotePods, cidrs, exceptRules int
	var netpolLatency bool
	var metricsProfiles []string
	var netpolReadyThreshold time.Duration
	var rc int
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		Run: func(cmd *cobra.Command, args []string) {
			// Register template functions used only by network-policy templates
			util.AddRenderingFunction("GetSubnet16", func(subnetIdx int) string {
				first := byte((subnetIdx >> 8) + 1)
				second := byte(subnetIdx & 0xFF)
				return netip.AddrFrom4([4]byte{first, second, 0, 0}).String() + "/16"
			})
			util.AddRenderingFunction("GetSubnet24In16", func(subnetIdx, offset int) string {
				first := byte((subnetIdx >> 8) + 1)
				second := byte(subnetIdx & 0xFF)
				third := byte(offset)
				return netip.AddrFrom4([4]byte{first, second, third, 0}).String() + "/24"
			})
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["PODS_PER_NAMESPACE"] = podsPerNamespace
			AdditionalVars["NETPOLS_PER_NAMESPACE"] = netpolPerNamespace
			AdditionalVars["LOCAL_PODS"] = localPods
			AdditionalVars["POD_SELECTORS"] = podSelectors
			AdditionalVars["SINGLE_PORTS"] = singlePorts
			AdditionalVars["PORT_RANGES"] = portRanges
			AdditionalVars["REMOTE_NAMESPACES"] = remoteNamespaces
			AdditionalVars["REMOTE_PODS"] = remotePods
			AdditionalVars["CIDRS"] = cidrs
			AdditionalVars["EXCEPT_RULES"] = exceptRules
			AdditionalVars["NETPOL_LATENCY"] = netpolLatency
			AdditionalVars["NETPOL_READY_THRESHOLD"] = netpolReadyThreshold

			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 10, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().DurationVar(&netpolReadyThreshold, "netpol-ready-threshold", 10*time.Second, "Network policy ready timeout threshold")
	cmd.Flags().IntVar(&podsPerNamespace, "pods-per-namespace", 10, "Number of pods created in a namespace")
	cmd.Flags().IntVar(&netpolPerNamespace, "netpol-per-namespace", 10, "Number of network policies created in a namespace")
	cmd.Flags().IntVar(&localPods, "local-pods", 2, "Number of pods on the local namespace to receive traffic from remote namespace pods")
	cmd.Flags().IntVar(&podSelectors, "pod-selectors", 1, "Number of pod and namespace selectors to be used in ingress and egress rules")
	cmd.Flags().IntVar(&singlePorts, "single-ports", 2, "Number of TCP ports to be used in ingress and egress rules")
	cmd.Flags().IntVar(&portRanges, "port-ranges", 2, "Number of TCP port ranges to be used in ingress and egress rules")
	cmd.Flags().IntVar(&remoteNamespaces, "remotes-namespaces", 2, "Number of remote namespaces to accept traffic from or send traffic to in ingress and egress rules")
	cmd.Flags().IntVar(&remotePods, "remotes-pods", 2, "Number of pods in remote namespaces to accept traffic from or send traffic to in ingress and egress rules")
	cmd.Flags().IntVar(&cidrs, "cidrs", 2, "Number of cidrs to accept traffic from or send traffic to in ingress and egress rules")
	cmd.Flags().IntVar(&exceptRules, "except-rules", 3, "Number of except rules to exclude traffic from ingress and egress cidr blocks")
	cmd.Flags().BoolVar(&netpolLatency, "networkpolicy-latency", true, "Enable network policy latency measurement")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics-aggregated.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
