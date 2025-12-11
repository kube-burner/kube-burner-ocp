// Copyright 2022 The Kube-burner Authors.
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

	"github.com/kube-burner/kube-burner/pkg/workloads"

	"github.com/spf13/cobra"
)

// NewWhereabouts
func NewWhereabouts(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations int
	var fast bool
	var podReadyThreshold, jobIterationDelay time.Duration
	var containerImage string
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "whereabouts",
		Short:        "Runs whereabouts workload",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["CONTAINER_IMAGE"] = containerImage
			AdditionalVars["FAST"] = fast
			AdditionalVars["JOB_ITERATION_DELAY"] = jobIterationDelay
			setMetrics(cmd, metricsProfiles)
			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 10, "Number of iterations - each iteration is 1 ns and 6 pods")
	cmd.Flags().BoolVar(&fast, "fast", false, "Use Fast IPAM")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 15*time.Second, "Pod ready timeout threshold")
	cmd.Flags().StringVar(&containerImage, "container-image", "gcr.io/google_containers/pause:3.1", "Container image")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics-aggregated.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().DurationVar(&jobIterationDelay, "job-iteration-delay", 0, "Delay between job iterations")
	return cmd
}
