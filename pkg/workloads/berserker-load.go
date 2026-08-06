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
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"github.com/spf13/cobra"
)

// NewBerserkerLoad holds berserker-load workload
func NewBerserkerLoad(wh *workloads.WorkloadHelper) *cobra.Command {
	var rc int
	var metricsProfiles []string
	var jobIterations int
	var daemonsetReplicas int
	var serviceReplicas int
	var churnCycles, churnPercent int
	var churnDuration, churnDelay, jobPause, maxWaitTimeout, podReadyThreshold time.Duration
	var deletionStrategy, churnMode string

	cmd := &cobra.Command{
		Use:          "berserker-load",
		Short:        "Runs berserker-load workload",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			AdditionalVars["JOB_ITERATIONS"] = jobIterations
			AdditionalVars["JOB_PAUSE"] = jobPause
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["CHURN_MODE"] = churnMode
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["MAX_WAIT_TIMEOUT"] = maxWaitTimeout
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold

			AdditionalVars["DAEMONSET_REPLICAS"] = daemonsetReplicas
			AdditionalVars["SERVICE_REPLICAS"] = serviceReplicas

			setMetrics(cmd, metricsProfiles)
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}

	cmd.Flags().IntVar(&jobIterations, "job-iterations", 5, "Number of job iterations to create")
	cmd.Flags().DurationVar(&jobPause, "job-pause", 0, "Duration to pause after creating resources")
	cmd.Flags().DurationVar(&maxWaitTimeout, "max-wait-timeout", 12*time.Minute, "Maximum time to wait for created objects to be ready")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 5*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.GVRDeletionStrategy, "GC deletion mode")

	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Number of churn cycles (0 = infinite when churn-duration > 0)")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 0, "Churn duration (0 disables churn)")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 10*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 80, "Percentage of job iterations to churn each round")
	cmd.Flags().StringVar(&churnMode, "churn-mode", string(config.ChurnNamespaces), "Churn mode: namespaces or objects")

	cmd.Flags().IntVar(&daemonsetReplicas, "daemonset-replicas", 6, "Number of berserker DaemonSets to create")
	cmd.Flags().IntVar(&serviceReplicas, "service-replicas", 6, "Number of services")

	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"},
		"Comma separated list of metrics profiles to use")

	return cmd
}
