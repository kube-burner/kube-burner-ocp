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
	"os"
	"strconv"
	"time"

	"github.com/kube-burner/kube-burner/pkg/workloads"
	"github.com/spf13/cobra"
)

// NewNetworkPolicy holds network-policy workload
func NewNetworkPolicy(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, podsPerNamespace, netpolPerNamespace, localPods, podSelectors, singlePorts, portRanges, remoteNamespaces, remotePods, cidrs int
	var netpolLatency bool
	var metricsProfiles []string
	var netpolReadyThreshold time.Duration
	var rc int
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		PreRun: func(cmd *cobra.Command, args []string) {
			os.Setenv("JOB_ITERATIONS", fmt.Sprint(iterations))
			os.Setenv("PODS_PER_NAMESPACE", fmt.Sprint(podsPerNamespace))
			os.Setenv("NETPOLS_PER_NAMESPACE", fmt.Sprint(netpolPerNamespace))
			os.Setenv("LOCAL_PODS", fmt.Sprint(localPods))
			os.Setenv("POD_SELECTORS", fmt.Sprint(podSelectors))
			os.Setenv("SINGLE_PORTS", fmt.Sprint(singlePorts))
			os.Setenv("PORT_RANGES", fmt.Sprint(portRanges))
			os.Setenv("REMOTE_NAMESPACES", fmt.Sprint(remoteNamespaces))
			os.Setenv("REMOTE_PODS", fmt.Sprint(remotePods))
			os.Setenv("CIDRS", fmt.Sprint(cidrs))
			os.Setenv("NETPOL_LATENCY", strconv.FormatBool(netpolLatency))
			os.Setenv("NETPOL_READY_THRESHOLD", fmt.Sprintf("%v", netpolReadyThreshold))
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			rc = wh.Run(cmd.Name())
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
	cmd.Flags().BoolVar(&netpolLatency, "networkpolicy-latency", true, "Enable network policy latency measurement")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics-aggregated.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
