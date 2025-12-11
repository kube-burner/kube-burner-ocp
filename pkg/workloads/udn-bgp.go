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

	kubeburnermeasurements "github.com/kube-burner/kube-burner/pkg/measurements"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	"github.com/spf13/cobra"

	"github.com/kube-burner/kube-burner-ocp/pkg/measurements"
)

var additionalMeasurementFactoryMap = map[string]kubeburnermeasurements.NewMeasurementFactory{
	"raLatency": measurements.NewRaLatencyMeasurementFactory,
}

// NewUdnBgp holds udn-bgp workload
func NewUdnBgp(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, namespacePerCudn int
	var jobIterationDelay time.Duration
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["NAMESPACES_PER_CUDN"] = namespacePerCudn
			AdditionalVars["JOB_ITERATION_DELAY"] = jobIterationDelay

			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, additionalMeasurementFactoryMap)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 10, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().IntVar(&namespacePerCudn, "namespaces-per-cudn", 1, "Number of namespaces sharing the same cluster udn")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().DurationVar(&jobIterationDelay, "job-iteration-delay", 0, "Delay between job iterations")
	return cmd
}
