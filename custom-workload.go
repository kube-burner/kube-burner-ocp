// Copyright 2024 The Kube-burner Authors.
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
	"strings"
	"time"

	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

func CustomWorkload(wh *workloads.WorkloadHelper) *cobra.Command {
	var churn, namespacedIterations, svcLatency bool
	var churnDelay, churnDuration, podReadyThreshold time.Duration
	var configFile, benchmarkName, churnDeletionStrategy string
	var iterations, churnPercent, churnCycles, iterationsPerNamespace, podsPerNode int
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Runs custom workload",
		PreRun: func(cmd *cobra.Command, args []string) {
			os.Setenv("CHURN", fmt.Sprint(churn))
			os.Setenv("CHURN_CYCLES", fmt.Sprintf("%v", churnCycles))
			os.Setenv("CHURN_DURATION", fmt.Sprintf("%v", churnDuration))
			os.Setenv("CHURN_DELAY", fmt.Sprintf("%v", churnDelay))
			os.Setenv("CHURN_PERCENT", fmt.Sprint(churnPercent))
			os.Setenv("CHURN_DELETION_STRATEGY", churnDeletionStrategy)
			ingressDomain, err := wh.MetadataAgent.GetDefaultIngressDomain()
			if err != nil {
				log.Fatal("Error obtaining default ingress domain: ", err.Error())
			}
			os.Setenv("INGRESS_DOMAIN", ingressDomain)
			os.Setenv("ITERATIONS_PER_NAMESPACE", fmt.Sprint(iterationsPerNamespace))
			if iterations > 0 {
				os.Setenv("JOB_ITERATIONS", fmt.Sprint(iterations))
			}
			if podsPerNode > 0 {
				totalPods := wh.ClusterMetadata.WorkerNodesCount * podsPerNode
				podCount, err := wh.MetadataAgent.GetCurrentPodCount()
				if err != nil {
					log.Fatal(err)
				}
				os.Setenv("JOB_ITERATIONS", fmt.Sprint((totalPods-podCount)/2))
			}
			os.Setenv("NAMESPACED_ITERATIONS", fmt.Sprint(namespacedIterations))
			os.Setenv("POD_READY_THRESHOLD", fmt.Sprintf("%v", podReadyThreshold))
			os.Setenv("SVC_LATENCY", strconv.FormatBool(svcLatency))
		},
		Run: func(cmd *cobra.Command, args []string) {
			if _, err := os.Stat(configFile); err != nil {
				log.Fatalf("Error reading custom configuration file: %v", err.Error())
			}
			configFileName := strings.Split(configFile, ".")[0]
			wh.Run(configFileName)
		},
	}
	cmd.Flags().StringVarP(&configFile, "config", "c", "", "Config file path or url")
	cmd.Flags().StringVarP(&benchmarkName, "benchmark", "b", "custom-workload", "Name of the benchmark")
	cmd.Flags().BoolVar(&churn, "churn", true, "Enable churning")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().StringVar(&churnDeletionStrategy, "churn-deletion-strategy", "default", "Churn deletion strategy to use")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 5*time.Minute, "Churn duration")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().IntVar(&iterations, "iterations", 1, "Job iterations. Mutually exclusive with '--pods-per-node'")
	cmd.Flags().IntVar(&iterationsPerNamespace, "iterations-per-namespace", 1, "Iterations per namespace")
	// Adding a super set of flags from other commands so users can decide if they want to use them
	cmd.Flags().BoolVar(&namespacedIterations, "namespaced-iterations", true, "Namespaced iterations")
	cmd.Flags().IntVar(&podsPerNode, "pods-per-node", 50, "Pods per node. Mutually exclusive with '--iterations'")
	cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	// pods-per-node calculates iterations, thus the two are mutually exclusive.
	cmd.MarkFlagsMutuallyExclusive("iterations", "pods-per-node")
	cmd.MarkFlagRequired("config")
	return cmd
}
