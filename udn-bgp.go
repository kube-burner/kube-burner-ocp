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

package ocp

import (
	"fmt"
	"os"
	"strings"

	"github.com/kube-burner/kube-burner/pkg/measurements"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	"github.com/spf13/cobra"
)

var additionalMeasurementFactoryMap = map[string]measurements.NewMeasurementFactory{
	"raLatency": NewRaLatencyMeasurementFactory,
}

// NewUdnBgp holds udn-bgp workload
func NewUdnBgp(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, namespacePerCudn int
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		PreRun: func(cmd *cobra.Command, args []string) {
			os.Setenv("JOB_ITERATIONS", fmt.Sprint(iterations))
			os.Setenv("NAMESPACES_PER_CUDN", fmt.Sprint(namespacePerCudn))
		},
		Run: func(cmd *cobra.Command, args []string) {
			os.Setenv("METRICS", strings.Join(metricsProfiles, ","))
			rc = wh.RunWithAdditionalVars(cmd.Name(), nil, additionalMeasurementFactoryMap)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 10, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().IntVar(&namespacePerCudn, "namespaces-per-cudn", 1, "Number of namespaces sharing the same cluster udn")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics-aggregated.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
