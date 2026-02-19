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

// NewUDNDensityPods holds udn-density-pods workload
func NewUDNDensityPods(wh *workloads.WorkloadHelper) *cobra.Command {
	var churnPercent, churnCycles, iterations int
	var l3, simple, pprof bool
	var jobPause time.Duration
	var churnDelay, churnDuration, podReadyThreshold time.Duration
	var deletionStrategy, churnMode string
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "udn-density-pods",
		Short:        "Runs node-density-udn workload",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			// Disable l3 when the user chooses l2
			if l3 {
				log.Info("Layer 3 is enabled")
			} else {
				log.Info("Layer 2 is enabled")
			}
			if churnDuration > 0 || churnCycles > 0 {
				log.Info("Churn is enabled, there will not be a pause after UDN creation")
			}

			AdditionalVars["PPROF"] = pprof
			AdditionalVars["JOB_PAUSE"] = jobPause
			AdditionalVars["SIMPLE"] = simple
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["CHURN_MODE"] = churnMode
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["ENABLE_LAYER_3"] = l3
			AddWorkloadFlagsToMetadata(cmd, wh)
			wh.SetVariables(AdditionalVars, SetVars)
			rc = wh.Run(cmd.Name() + ".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().BoolVar(&l3, "layer3", true, "Layer3 UDN test")
	cmd.Flags().DurationVar(&jobPause, "job-pause", 0, "Time to pause after finishing the job that creates the UDN")
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Enable pprof collection")
	cmd.Flags().BoolVar(&simple, "simple", false, "only client and server pods to be deployed, no services and networkpolicies")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 0, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&churnMode, "churn-mode", string(config.ChurnNamespaces), "Either namespaces, to churn entire namespaces or objects, to churn individual objects")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.DefaultDeletionStrategy, "GC deletion mode, default deletes entire namespaces and gvr deletes objects within namespaces before deleting the parent namespace")
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Job iterations")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 1*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
