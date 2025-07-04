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
	"os"
	"time"

	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

func CustomWorkload(wh *workloads.WorkloadHelper) *cobra.Command {
	var churn, namespacedIterations, svcLatency bool
	var churnDelay, churnDuration, podReadyThreshold time.Duration
	var configFile, churnDeletionStrategy string
	var iterations, churnPercent, churnCycles, iterationsPerNamespace, podsPerNode int
	var rc int
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Runs custom workload",
		Run: func(cmd *cobra.Command, args []string) {
			var jobIterations int
			if _, err := os.Stat(configFile); err != nil {
				log.Fatalf("Error reading custom configuration file: %v", err.Error())
			}

			ingressDomain, err := wh.MetadataAgent.GetDefaultIngressDomain()
			if err != nil {
				log.Fatal("Error obtaining default ingress domain: ", err.Error())
			}

			if iterations > 0 {
				jobIterations = iterations
			}
			if podsPerNode > 0 {
				totalPods := clusterMetadata.WorkerNodesCount * podsPerNode
				podCount, err := wh.MetadataAgent.GetCurrentPodCount()
				if err != nil {
					log.Fatal(err)
				}
				jobIterations = (totalPods - podCount) / 2
			}

			additionalVars := map[string]any{
				"CHURN":                    churn,
				"CHURN_CYCLES":             churnCycles,
				"CHURN_DURATION":           churnDuration,
				"CHURN_DELAY":              churnDelay,
				"CHURN_PERCENT":            churnPercent,
				"CHURN_DELETION_STRATEGY":  churnDeletionStrategy,
				"INGRESS_DOMAIN":           ingressDomain,
				"ITERATIONS_PER_NAMESPACE": iterationsPerNamespace,
				"JOB_ITERATIONS":           jobIterations,
				"NAMESPACED_ITERATIONS":    namespacedIterations,
				"POD_READY_THRESHOLD":      podReadyThreshold,
				"SVC_LATENCY":              svcLatency,
			}
			rc = wh.RunWithAdditionalVars(configFile, additionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVarP(&configFile, "config", "c", "", "Config file path or url")
	cmd.Flags().BoolVar(&churn, "churn", true, "Enable churning")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().StringVar(&churnDeletionStrategy, "churn-deletion-strategy", "default", "Churn deletion strategy to use")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 5*time.Minute, "Churn duration")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Job iterations. Mutually exclusive with '--pods-per-node'")
	cmd.Flags().IntVar(&iterationsPerNamespace, "iterations-per-namespace", 1, "Iterations per namespace")
	// Adding a super set of flags from other commands so users can decide if they want to use them
	cmd.Flags().BoolVar(&namespacedIterations, "namespaced-iterations", true, "Namespaced iterations")
	cmd.Flags().IntVar(&podsPerNode, "pods-per-node", 0, "Pods per node. Mutually exclusive with '--iterations'")
	cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	// pods-per-node calculates iterations, thus the two are mutually exclusive.
	cmd.MarkFlagsMutuallyExclusive("iterations", "pods-per-node")
	cmd.MarkFlagRequired("config")
	return cmd
}
