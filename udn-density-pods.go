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
	"strings"
	"time"

	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// NewUDNDensityPods holds udn-density-pods workload
func NewUDNDensityPods(wh *workloads.WorkloadHelper) *cobra.Command {
	var churnPercent, churnCycles, iterations int
	var churn, l3, simple, pprof bool
	var churnDelay, churnDuration, podReadyThreshold time.Duration
	var churnDeletionStrategy, jobPause string
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "udn-density-pods",
		Short:        "Runs node-density-udn workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			os.Setenv("JOB_PAUSE", jobPause)
			os.Setenv("PPROF", fmt.Sprint(pprof))
			os.Setenv("SIMPLE", fmt.Sprint(simple))
			os.Setenv("CHURN", fmt.Sprint(churn))
			os.Setenv("CHURN_CYCLES", fmt.Sprintf("%v", churnCycles))
			os.Setenv("CHURN_DURATION", fmt.Sprintf("%v", churnDuration))
			os.Setenv("CHURN_DELAY", fmt.Sprintf("%v", churnDelay))
			os.Setenv("CHURN_PERCENT", fmt.Sprint(churnPercent))
			os.Setenv("CHURN_DELETION_STRATEGY", churnDeletionStrategy)
			os.Setenv("JOB_ITERATIONS", fmt.Sprint(iterations))
			os.Setenv("POD_READY_THRESHOLD", fmt.Sprintf("%v", podReadyThreshold))
		},
		Run: func(cmd *cobra.Command, args []string) {
			os.Setenv("METRICS", strings.Join(metricsProfiles, ","))
			// Disable l3 when the user chooses l2
			if l3 {
				log.Info("Layer 3 is enabled")
				os.Setenv("ENABLE_LAYER_3", "true")
			} else {
				log.Info("Layer 2 is enabled")
				os.Setenv("ENABLE_LAYER_3", "false")
			}
			if churn {
				log.Info("Churn is enabled, there will not be a pause after UDN creation")
			}
			rc = wh.Run("udn-density-pods")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().BoolVar(&l3, "layer3", true, "Layer3 UDN test")
	cmd.Flags().StringVar(&jobPause, "job-pause", "1ms", "Time to pause after finishing the job")
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
