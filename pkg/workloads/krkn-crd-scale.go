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
	"os"
	"strconv"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"github.com/spf13/cobra"
)

// NewKrknCrdScale holds the Krkn CRD scale workload.
func NewKrknCrdScale(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations, churnCycles, churnPercent int
	var namespace, churnMode, deletionStrategy string
	var churnDelay, churnDuration time.Duration
	var reconcileP99Threshold time.Duration
	var waitForConvergence bool
	var operatorNamespace string
	var metricsProfiles []string
	var rc int

	cmd := &cobra.Command{
		Use:          "krkn-crd-scale",
		Short:        "Runs the Krkn operator CRD scale workload",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			_ = os.Setenv("OPERATOR_NAMESPACE", operatorNamespace)
			_ = os.Setenv("RECONCILE_P99_THRESHOLD", strconv.FormatFloat(reconcileP99Threshold.Seconds(), 'f', -1, 64))
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["NAMESPACE"] = namespace
			AdditionalVars["OPERATOR_NAMESPACE"] = operatorNamespace
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["CHURN_MODE"] = churnMode
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["WAIT_FOR_CONVERGENCE"] = waitForConvergence
			AdditionalVars["RECONCILE_P99_THRESHOLD"] = reconcileP99Threshold.Seconds()
			if alerts, ok := AdditionalVars["ALERTS"].(string); ok && alerts != "" {
				AdditionalVars["ALERTS"] = "krkn-operator-alerts.yml"
			}

			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}

	cmd.Flags().IntVar(&iterations, "iterations", 0, "Number of Krkn object bundles to create; each bundle creates 28 objects")
	cmd.Flags().StringVar(&namespace, "namespace", "krkn-operator-system", "Namespace in which namespaced Krkn resources are created")
	cmd.Flags().StringVar(&operatorNamespace, "operator-namespace", "krkn-operator-system", "Namespace where the Krkn operator metrics are exposed")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 0, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between churn cycles")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations to churn each round")
	cmd.Flags().StringVar(&churnMode, "churn-mode", "objects", "Churn individual objects; namespace churn is intentionally not the default for this workload")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.DefaultDeletionStrategy, "Garbage-collection deletion mode")
	cmd.Flags().BoolVar(&waitForConvergence, "wait-for-convergence", false, "Wait for provider configuration resources to reach Completed; requires active providers")
	cmd.Flags().DurationVar(&reconcileP99Threshold, "reconcile-p99-threshold", 2*time.Second, "Warning threshold for p99 Krkn controller reconciliation latency")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"krkn-operator-metrics.yml", "metrics.yml"}, "Comma separated list of metrics profiles to use")
	_ = cmd.MarkFlagRequired("iterations")
	return cmd
}
