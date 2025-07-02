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

// NewUDNDensityPods holds udn-density-pods workload
func NewUDNDensityPods(wh *workloads.WorkloadHelper) *cobra.Command {
	var churnPercent, churnCycles, iterations int
	var churn, l3, simple, pprof bool
	var jobPause time.Duration
	var churnDelay, churnDuration, podReadyThreshold time.Duration
	var churnDeletionStrategy string
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
			if churn {
				log.Info("Churn is enabled, there will not be a pause after UDN creation")
			}

			additionalVars := map[string]any{
				"PPROF":                   pprof,
				"SIMPLE":                  simple,
				"CHURN":                   churn,
				"CHURN_CYCLES":            churnCycles,
				"CHURN_DURATION":          churnDuration,
				"CHURN_DELAY":             churnDelay,
				"CHURN_PERCENT":           churnPercent,
				"CHURN_DELETION_STRATEGY": churnDeletionStrategy,
				"JOB_ITERATIONS":          iterations,
				"POD_READY_THRESHOLD":     podReadyThreshold,
				"ENABLE_LAYER_3":          l3,
			}

			rc = wh.RunWithAdditionalVars("udn-density-pods.yml", additionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().BoolVar(&l3, "layer3", true, "Layer3 UDN test")
	cmd.Flags().DurationVar(&jobPause, "job-pause", 0, "Time to pause after finishing the job that creates the UDN")
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Enable pprof collection")
	cmd.Flags().BoolVar(&simple, "simple", false, "only client and server pods to be deployed, no services and networkpolicies")
	cmd.Flags().BoolVar(&churn, "churn", false, "Enable churning")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 1*time.Hour, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&churnDeletionStrategy, "churn-deletion-strategy", "default", "Churn deletion strategy to use")
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Iterations")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 1*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
