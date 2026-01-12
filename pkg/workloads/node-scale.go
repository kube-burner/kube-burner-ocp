// Copyright 2025 The Kube-burner Authors.
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
	"os"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"

	"github.com/spf13/cobra"
)

// NewNodeScale holds node-scale workload
func NewNodeScale(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var rc int
	var metricsProfiles []string
	var iterations, churnCycles, churnPercent, cpu, memory, maxPods int
	var podReadyThreshold, churnDuration, churnDelay, probesPeriod time.Duration
	var deletionStrategy, tag, churnMode string
	cmd := &cobra.Command{
		Use:          variant,
		Short:        fmt.Sprintf("Runs %v workload", variant),
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["CHURN_MODE"] = churnMode
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["PROBES_PERIOD"] = probesPeriod.Seconds()
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["CPU"] = cpu
			AdditionalVars["MEMORY"] = memory
			AdditionalVars["TAG"] = tag
			AdditionalVars["MAX_PODS"] = maxPods
			setMetrics(cmd, metricsProfiles)
			wh.SetVariables(AdditionalVars, SetVars)
			rc = wh.Run(cmd.Name() + ".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 0, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().StringVar(&churnMode, "churn-mode", string(config.ChurnObjects), "Either namespaces, to churn entire namespaces or objects, to churn individual objects")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.GVRDeletionStrategy, "GC deletion mode, default deletes entire namespaces and gvr deletes objects within namespaces before deleting the parent namespace")
	cmd.Flags().StringVar(&tag, "version", "v1.33.0", "Image tag version of the kubemark container")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Number of iterations/namespaces")
	cmd.Flags().IntVar(&cpu, "cpu", 1, "CPU capacity of each hollow node")
	cmd.Flags().IntVar(&memory, "memory", 4, "Memory (G) of each hollow node")
	cmd.Flags().IntVar(&maxPods, "max-pods", 250, "Max number of pods of each hollow node")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 2*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
