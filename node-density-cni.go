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

// NewNodeDensity holds node-density-cni workload
func NewNodeDensityCNI(wh *workloads.WorkloadHelper) *cobra.Command {
	var podsPerNode, iterationsPerNamespace, churnCycles, churnPercent int
	var churnDelay, churnDuration time.Duration
	var churnDeletionStrategy string
	var churn, namespacedIterations, svcLatency bool
	var podReadyThreshold time.Duration
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "node-density-cni",
		Short:        "Runs node-density-cni workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			totalPods := clusterMetadata.WorkerNodesCount * podsPerNode
			podCount, err := wh.MetadataAgent.GetCurrentPodCount()
			if err != nil {
				log.Fatal(err)
			}

			os.Setenv("CHURN", fmt.Sprint(churn))
			os.Setenv("CHURN_CYCLES", fmt.Sprintf("%v", churnCycles))
			os.Setenv("CHURN_DURATION", fmt.Sprintf("%v", churnDuration))
			os.Setenv("CHURN_DELAY", fmt.Sprintf("%v", churnDelay))
			os.Setenv("CHURN_PERCENT", fmt.Sprint(churnPercent))
			os.Setenv("CHURN_DELETION_STRATEGY", churnDeletionStrategy)
			os.Setenv("JOB_ITERATIONS", fmt.Sprint((totalPods-podCount)/2))
			os.Setenv("NAMESPACED_ITERATIONS", fmt.Sprint(namespacedIterations))
			os.Setenv("ITERATIONS_PER_NAMESPACE", fmt.Sprint(iterationsPerNamespace))
			os.Setenv("POD_READY_THRESHOLD", fmt.Sprintf("%v", podReadyThreshold))
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
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 1*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().IntVar(&podsPerNode, "pods-per-node", 245, "Pods per node")
	cmd.Flags().BoolVar(&namespacedIterations, "namespaced-iterations", true, "Namespaced iterations")
	cmd.Flags().IntVar(&iterationsPerNamespace, "iterations-per-namespace", 1000, "Iterations per namespace")
	cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
