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

package workloads

import (
	"fmt"
	"net/netip"
	"os"
	"time"

	"github.com/kube-burner/kube-burner-ocp/pkg/measurements"
	kubeburnermeasurements "github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/util"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"github.com/spf13/cobra"
)

// NewNetworkPolicy holds network-policy workload
func NewNetworkPolicy(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, podsPerNamespace, netpolPerNamespace, localPods, podSelectors, singlePorts, portRanges, remoteNamespaces, remotePods, cidrs, exceptRules int
	var vlans, namespacesPerVlan, vmsPerNamespace int
	var netpolLatency, multiNetworkPolicy, virt bool
	var metricsProfiles []string
	var netpolReadyThreshold, vmStartupPause time.Duration
	var rc int

	mnpMeasurementFactoryMap := map[string]kubeburnermeasurements.NewMeasurementFactory{
		"mnpLatency": measurements.NewMnpLatencyMeasurementFactory,
	}
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if exceptRules > 0 && netpolLatency {
				return fmt.Errorf("cannot use --except-rules > 0 with --networkpolicy-latency=true: network policy latency measurement does not work correctly with except rules")
			}
			if multiNetworkPolicy && netpolLatency {
				return fmt.Errorf("cannot use --multi-network-policy with --networkpolicy-latency=true: network policy latency measurement is not supported for MultiNetworkPolicy, use --networkpolicy-latency=false")
			}
			if virt && !multiNetworkPolicy {
				return fmt.Errorf("--virt requires --multi-network-policy: VMs use secondary interfaces which require MultiNetworkPolicy")
			}
			if multiNetworkPolicy {
				if namespacesPerVlan <= 0 {
					return fmt.Errorf("--namespaces-per-vlan must be greater than 0")
				}
				requiredVlans := (iterations + namespacesPerVlan - 1) / namespacesPerVlan
				if vlans < requiredVlans {
					return fmt.Errorf("--vlans %d is too few for --iterations %d with --namespaces-per-vlan %d (need at least %d VLANs)", vlans, iterations, namespacesPerVlan, requiredVlans)
				}
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
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
			AdditionalVars["MULTI_NETWORK_POLICY"] = multiNetworkPolicy
			AdditionalVars["VIRT"] = virt
			AdditionalVars["VLANS"] = vlans
			AdditionalVars["NAMESPACES_PER_VLAN"] = namespacesPerVlan
			AdditionalVars["VMS_PER_NAMESPACE"] = vmsPerNamespace
			if virt {
				AdditionalVars["PODS_PER_NAMESPACE"] = vmsPerNamespace
				AdditionalVars["VM_STARTUP_PAUSE"] = vmStartupPause
			}

			if multiNetworkPolicy {
				wh.SetMeasurements(mnpMeasurementFactoryMap)
			}
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 10, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().DurationVar(&netpolReadyThreshold, "netpol-ready-threshold", 0, "Network policy ready timeout threshold")
	cmd.Flags().IntVar(&podsPerNamespace, "pods-per-namespace", 10, "Number of pods created in a namespace")
	cmd.Flags().IntVar(&netpolPerNamespace, "netpol-per-namespace", 10, "Number of network policies created in a namespace")
	cmd.Flags().IntVar(&localPods, "local-pods", 2, "Number of pods on the local namespace to receive traffic from remote namespace pods")
	cmd.Flags().IntVar(&podSelectors, "pod-selectors", 1, "Number of pod and namespace selectors to be used in ingress and egress rules")
	cmd.Flags().IntVar(&singlePorts, "single-ports", 2, "Number of TCP ports to be used in ingress and egress rules")
	cmd.Flags().IntVar(&portRanges, "port-ranges", 2, "Number of TCP port ranges to be used in ingress and egress rules")
	cmd.Flags().IntVar(&remoteNamespaces, "remotes-namespaces", 2, "Number of remote namespaces to accept traffic from or send traffic to in ingress and egress rules")
	cmd.Flags().IntVar(&remotePods, "remotes-pods", 2, "Number of pods in remote namespaces to accept traffic from or send traffic to in ingress and egress rules")
	cmd.Flags().IntVar(&cidrs, "cidrs", 2, "Number of cidrs to accept traffic from or send traffic to in ingress and egress rules")
	cmd.Flags().IntVar(&exceptRules, "except-rules", 0, "Number of except rules to exclude traffic from ingress and egress cidr blocks")
	cmd.Flags().BoolVar(&netpolLatency, "networkpolicy-latency", true, "Enable network policy latency measurement")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics-aggregated.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().BoolVar(&multiNetworkPolicy, "multi-network-policy", false, "Enable multi network policy for secondary interfaces")
	cmd.Flags().BoolVar(&virt, "virt", false, "Use virtual machines instead of pods (requires --multi-network-policy)")
	cmd.Flags().IntVar(&vlans, "vlans", 10, "Number of localnet VLANs created on br-ex using NNCP")
	cmd.Flags().IntVar(&namespacesPerVlan, "namespaces-per-vlan", 2, "Number of namespaces sharing the same VLAN")
	cmd.Flags().IntVar(&vmsPerNamespace, "vms-per-namespace", 10, "Number of VMs created in a namespace")
	cmd.Flags().DurationVar(&vmStartupPause, "vm-startup-pause", 2*time.Minute, "How long to wait after VM creation before proceeding (--virt mode only, replaces waitWhenFinished due to CNV status bug)")
	return cmd
}
