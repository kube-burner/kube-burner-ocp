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
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NewNodeDensity holds node-density workload
func NewNodeDensity(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var rc int
	var metricsProfiles []string
	var iterationsPerNamespace, podsPerNode, churnCycles, churnPercent, numSriovs int
	var podReadyThreshold, churnDuration, churnDelay, probesPeriod, pprofInterval time.Duration
	var containerImage, deletionStrategy, churnMode, selector, perfProfile, sriovNetworkName string
	var namespacedIterations, pprof, svcLatency bool
	cmd := &cobra.Command{
		Use:          variant,
		Short:        fmt.Sprintf("Runs %v workload", variant),
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			kubeClientProvider := config.NewKubeClientProvider("", "")
			clientSet, _ := kubeClientProvider.ClientSet(0, 0)
			nodes, err := clientSet.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{LabelSelector: selector})
			if err != nil {
				log.Fatal(err.Error())
			}
			if len(nodes.Items) == 0 {
				log.Fatalf("No nodes found with the selector: %s", selector)
			}
			totalPods := len(nodes.Items) * podsPerNode
			podCount, err := wh.MetadataAgent.GetCurrentPodCount(selector)
			if err != nil {
				log.Fatal(err.Error())
			}
			nodeSelectorJSON, err := buildNodeSelectorJSON(selector)
			if err != nil {
				log.Fatal(err.Error())
			}
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["CHURN_MODE"] = churnMode
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["PROBES_PERIOD"] = probesPeriod.Seconds()
			AdditionalVars["CONTAINER_IMAGE"] = containerImage
			AdditionalVars["SVC_LATENCY"] = svcLatency
			AdditionalVars["NAMESPACED_ITERATIONS"] = namespacedIterations
			AdditionalVars["ITERATIONS_PER_NAMESPACE"] = iterationsPerNamespace
			AdditionalVars["PPROF"] = pprof
			AdditionalVars["PPROF_INTERVAL"] = pprofInterval.String()
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["PERF_PROFILE"] = perfProfile
			AdditionalVars["NUM_SRIOVS"] = numSriovs
			AdditionalVars["SRIOV_NETWORK_NAME"] = sriovNetworkName
			AdditionalVars["NODE_SELECTOR"] = nodeSelectorJSON
			if variant == "node-density" {
				AdditionalVars["JOB_ITERATIONS"] = totalPods - podCount
			} else {
				AdditionalVars["JOB_ITERATIONS"] = (totalPods - podCount) / 2
			}
			setMetrics(cmd, metricsProfiles)
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 0, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.GVRDeletionStrategy, "GC deletion mode, default deletes entire namespaces and gvr deletes objects within namespaces before deleting the parent namespace")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&churnMode, "churn-mode", string(config.ChurnObjects), "Either namespaces, to churn entire namespaces or objects, to churn individual objects")
	cmd.Flags().IntVar(&podsPerNode, "pods-per-node", 245, "Pods per node")
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Enable pprof collection")
	cmd.Flags().DurationVar(&pprofInterval, "pprof-interval", 0, "Interval between pprof collections")
	switch variant {
	case "node-density":
		cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 0, "Pod ready timeout threshold")
		cmd.Flags().StringVar(&containerImage, "container-image", "gcr.io/google_containers/pause:3.1", "Container image")
	case "node-density-heavy":
		cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 0, "Pod ready timeout threshold")
		cmd.Flags().DurationVar(&probesPeriod, "probes-period", 10*time.Second, "Perf app readiness/liveness probes period")
		cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	case "node-density-cni":
		cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 0, "Pod ready timeout threshold")
		cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
		cmd.Flags().IntVar(&numSriovs, "num-sriovs", 0, "Number of SR-IOV interfaces per pod")
		cmd.Flags().StringVar(&sriovNetworkName, "sriov-networkname", "sriov-net", "SR-IOV network name for IPAM configuration")
		cmd.Flags().StringVar(&perfProfile, "perf-profile", "", "Performance profile name in the Cluster")
	}
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().BoolVar(&namespacedIterations, "namespaced-iterations", true, "Namespaced iterations")
	cmd.Flags().IntVar(&iterationsPerNamespace, "iterations-per-namespace", 1000, "Iterations per namespace")
	cmd.Flags().StringVar(&selector, "selector", WorkerNodeSelector, "Node selector")
	return cmd
}
