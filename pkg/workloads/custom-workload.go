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

package workloads

import (
	"os"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

func CustomWorkload(wh *workloads.WorkloadHelper) *cobra.Command {
	var namespacedIterations, svcLatency, pprof bool
	var churnDelay, churnDuration, podReadyThreshold, pprofInterval time.Duration
	var configFile, deletionStrategy, churnMode, selector string
	var iterations, churnPercent, churnCycles, iterationsPerNamespace, podsPerNode int
	var metricsProfiles []string
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
				podCount, err := wh.MetadataAgent.GetCurrentPodCount(selector)
				if err != nil {
					log.Fatal(err)
				}
				jobIterations = (totalPods - podCount) / 2
			}

			AdditionalVars["INGRESS_DOMAIN"] = ingressDomain
			if cmd.Flags().Changed("iterations") || cmd.Flags().Changed("pods-per-node") {
				AdditionalVars["JOB_ITERATIONS"] = jobIterations
			}
			setIfFlagChanged(cmd, map[string]any{
				"churn-cycles":             churnCycles,
				"churn-duration":           churnDuration,
				"churn-delay":              churnDelay,
				"churn-percent":            churnPercent,
				"churn-mode":               churnMode,
				"deletion-strategy":        deletionStrategy,
				"iterations-per-namespace": iterationsPerNamespace,
				"namespaced-iterations":    namespacedIterations,
				"pod-ready-threshold":      podReadyThreshold,
				"selector":                 selector,
				"pprof":                    pprof,
				"pprof-interval":           pprofInterval.String(),
			})
			if cmd.Flags().Changed("service-latency") {
				AdditionalVars["SVC_LATENCY"] = svcLatency
			}

			setMetrics(cmd, metricsProfiles)
			rc = RunWorkload(cmd, wh, configFile)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVarP(&configFile, "config", "c", "", "Config file path or url")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.DefaultDeletionStrategy, "GC deletion mode, default deletes entire namespaces and gvr deletes objects within namespaces before deleting the parent namespace")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 0, "Churn duration")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&churnMode, "churn-mode", string(config.ChurnNamespaces), "Either namespaces, to churn entire namespaces or objects, to churn individual objects")
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Job iterations. Mutually exclusive with '--pods-per-node'")
	cmd.Flags().IntVar(&iterationsPerNamespace, "iterations-per-namespace", 1, "Iterations per namespace")
	// Adding a super set of flags from other commands so users can decide if they want to use them
	cmd.Flags().BoolVar(&namespacedIterations, "namespaced-iterations", true, "Namespaced iterations")
	cmd.Flags().IntVar(&podsPerNode, "pods-per-node", 0, "Pods per node. Mutually exclusive with '--iterations'")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 0, "Pod ready timeout threshold")
	cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Enable pprof collection")
	cmd.Flags().DurationVar(&pprofInterval, "pprof-interval", 0, "Interval between pprof collections")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	// pods-per-node calculates iterations, thus the two are mutually exclusive.
	cmd.MarkFlagsMutuallyExclusive("iterations", "pods-per-node")
	cmd.Flags().StringVar(&selector, "selector", "", "Node selector")
	cmd.MarkFlagRequired("config")
	return cmd
}
