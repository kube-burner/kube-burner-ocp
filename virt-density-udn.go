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
	var churnDeletionStrategy, jobPause, vmImage, bindingMethod string
	var rc int
	cmd := &cobra.Command{
		Use:          "virt-udn-density",
		Short:        "Runs virt-density-udn workload",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			if bindingMethod != "passt" && bindingMethod != "l2bridge" {
				fmt.Println("Invalid value for --binding-method. Allowed values are 'passt' or 'l2bridge'.")
				os.Exit(1)
			}
			setMetrics(cmd, metricsProfiles)

			AdditionalVars["JOB_PAUSE"] = jobPause
			AdditionalVars["CHURN"] = churn
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["CHURN_DELETION_STRATEGY"] = churnDeletionStrategy
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["VMI_RUNNING_THRESHOLD"] = vmiRunningThreshold
			AdditionalVars["VM_IMAGE"] = vmImage
			AdditionalVars["UDN_BINDING_METHOD"] = bindingMethod
			AdditionalVars["ENABLE_LAYER_3"] = l3

			if l3 {
				log.Info("Layer 3 is enabled")
				AddVirtMetadata(wh, vmImage, "layer3", bindingMethod)
			} else {
				log.Info("Layer 2 is enabled")
				AddVirtMetadata(wh, vmImage, "layer2", bindingMethod)
			}
			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().BoolVar(&l3, "layer3", false, "Enable Layer3 UDN instead of Layer2, default: false - layer2 enabled")
	cmd.Flags().BoolVar(&churn, "churn", false, "Enable churning")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().StringVar(&jobPause, "job-pause", "1ms", "Time to pause after finishing the job")
	cmd.Flags().StringVar(&vmImage, "vm-image", "quay.io/openshift-cnv/qe-cnv-tests-fedora:40", "Vm Image to be deployed")
	cmd.Flags().StringVar(&bindingMethod, "binding-method", "l2bridge", "Binding method for the VM UDN network interface - acceptable values: 'l2bridge' | 'passt'")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 1*time.Hour, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&churnDeletionStrategy, "churn-deletion-strategy", "default", "Churn deletion strategy to use")
	cmd.Flags().IntVar(&iterations, "iteration", 1, "iterations")
	cmd.Flags().DurationVar(&vmiRunningThreshold, "vmi-ready-threshold", 60*time.Second, "VMI ready timeout threshold")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
