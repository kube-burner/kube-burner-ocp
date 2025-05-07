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
	log "github.com/sirupsen/logrus"

	"github.com/spf13/cobra"
)

// NewNodeDensity holds node-density workload
func NewNodeDensity(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var rc int
	var metricsProfiles []string
	var iterationsPerNamespace, podsPerNode, churnCycles, churnPercent int
	var podReadyThreshold, churnDuration, churnDelay, probesPeriod time.Duration
	var containerImage, churnDeletionStrategy string
	var namespacedIterations, churn, pprof, svcLatency bool
	cmd := &cobra.Command{
		Use:          variant,
		Short:        fmt.Sprintf("Runs %v workload", variant),
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			totalPods := clusterMetadata.WorkerNodesCount * podsPerNode
			podCount, err := wh.MetadataAgent.GetCurrentPodCount()
			if err != nil {
				log.Fatal(err.Error())
			}
			os.Setenv("CHURN", fmt.Sprint(churn))
			os.Setenv("CHURN_CYCLES", fmt.Sprintf("%v", churnCycles))
			os.Setenv("CHURN_DURATION", fmt.Sprintf("%v", churnDuration))
			os.Setenv("CHURN_DELAY", fmt.Sprintf("%v", churnDelay))
			os.Setenv("CHURN_PERCENT", fmt.Sprint(churnPercent))
			os.Setenv("CHURN_DELETION_STRATEGY", churnDeletionStrategy)
			if variant == "node-density" {
				os.Setenv("JOB_ITERATIONS", fmt.Sprint(totalPods-podCount))
			} else {
				os.Setenv("JOB_ITERATIONS", fmt.Sprint((totalPods-podCount)/2))
			}
			os.Setenv("NAMESPACED_ITERATIONS", fmt.Sprint(namespacedIterations))
			os.Setenv("ITERATIONS_PER_NAMESPACE", fmt.Sprint(iterationsPerNamespace))
			os.Setenv("PPROF", fmt.Sprint(pprof))
			os.Setenv("POD_READY_THRESHOLD", fmt.Sprintf("%v", podReadyThreshold))
			os.Setenv("PROBES_PERIOD", fmt.Sprint(probesPeriod.Seconds()))
			os.Setenv("CONTAINER_IMAGE", containerImage)
			os.Setenv("SVC_LATENCY", strconv.FormatBool(svcLatency))
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			rc = wh.Run(cmd.Name())
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().BoolVar(&churn, "churn", false, "Enable churning")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 1*time.Hour, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().StringVar(&churnDeletionStrategy, "churn-deletion-strategy", "gvr", "Churn deletion strategy to use")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().IntVar(&podsPerNode, "pods-per-node", 245, "Pods per node")
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Enable pprof collection")
	switch variant {
	case "node-density":
		cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 15*time.Second, "Pod ready timeout threshold")
		cmd.Flags().StringVar(&containerImage, "container-image", "gcr.io/google_containers/pause:3.1", "Container image")
	case "node-density-heavy":
		cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 2*time.Minute, "Pod ready timeout threshold")
		cmd.Flags().DurationVar(&probesPeriod, "probes-period", 10*time.Second, "Perf app readiness/liveness probes period")
		cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	case "node-density-cni":
		cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 1*time.Minute, "Pod ready timeout threshold")
		cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	}
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().BoolVar(&namespacedIterations, "namespaced-iterations", true, "Namespaced iterations")
	cmd.Flags().IntVar(&iterationsPerNamespace, "iterations-per-namespace", 1000, "Iterations per namespace")
	return cmd
}
