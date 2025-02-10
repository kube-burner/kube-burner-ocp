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
	"time"

	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// Returns virt-density workload
func NewVirtUDNDensity(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations int
	var vmiRunningThreshold time.Duration
	var metricsProfiles []string
	var churnPercent, churnCycles int
	var churn, l3 bool
	var churnDelay, churnDuration time.Duration
	var churnDeletionStrategy, jobPause, bindingMethod string
	var rc int
	cmd := &cobra.Command{
		Use:          "virt-udn-density",
		Short:        "Runs virt-density workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			os.Setenv("JOB_PAUSE", jobPause)
			os.Setenv("CHURN", fmt.Sprint(churn))
			os.Setenv("CHURN_CYCLES", fmt.Sprintf("%v", churnCycles))
			os.Setenv("CHURN_DURATION", fmt.Sprintf("%v", churnDuration))
			os.Setenv("CHURN_DELAY", fmt.Sprintf("%v", churnDelay))
			os.Setenv("CHURN_PERCENT", fmt.Sprint(churnPercent))
			os.Setenv("CHURN_DELETION_STRATEGY", churnDeletionStrategy)
			os.Setenv("JOB_ITERATIONS", fmt.Sprint(iterations))
			os.Setenv("VMI_RUNNING_THRESHOLD", fmt.Sprintf("%v", vmiRunningThreshold))
			os.Setenv("UDN_BINDING_METHOD", bindingMethod)
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			if l3 {
				log.Info("Layer 3 is enabled")
				os.Setenv("ENABLE_LAYER_3", "true")
			} else {
				log.Info("Layer 2 is enabled")
				os.Setenv("ENABLE_LAYER_3", "false")
			}
			rc = wh.Run(cmd.Name())
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().BoolVar(&l3, "layer3", true, "Layer3 UDN test")
	cmd.Flags().BoolVar(&churn, "churn", false, "Enable churning")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().StringVar(&jobPause, "job-pause", "1ms", "Time to pause after finishing the job")
	cmd.Flags().StringVar(&bindingMethod, "binding-method", "passt", "Binding method for the VM UDN network interface")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 1*time.Hour, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&churnDeletionStrategy, "churn-deletion-strategy", "default", "Churn deletion strategy to use")
	cmd.Flags().IntVar(&iterations, "iteration", 1, "iterations")
	cmd.Flags().DurationVar(&vmiRunningThreshold, "vmi-ready-threshold", 20*time.Second, "VMI ready timeout threshold")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
