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
	"os"
	"time"

	kubeburnermeasurements "github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"github.com/spf13/cobra"

	"github.com/kube-burner/kube-burner-ocp/pkg/measurements"
)

var icntMeasurementFactoryMap = map[string]kubeburnermeasurements.NewMeasurementFactory{
	"cudnLatency": measurements.NewCudnLatencyMeasurementFactory,
	"cncLatency":  measurements.NewCncLatencyMeasurementFactory,
}

// NewUDNICNT holds udn-icnt workload
func NewUDNICNT(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations int
	var pprof bool
	var connectivity string
	var podReadyThreshold, pprofInterval, jobPause time.Duration
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "udn-icnt",
		Short:        "Runs udn-icnt workload testing ClusterNetworkConnect between CUDN pairs",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["PPROF"] = pprof
			AdditionalVars["PPROF_INTERVAL"] = pprofInterval.String()
			AdditionalVars["JOB_PAUSE"] = jobPause
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["CONNECTIVITY"] = connectivity
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			wh.SetMeasurements(icntMeasurementFactoryMap)
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Number of CUDN pairs to create (each pair = 2 namespaces + 2 CUDNs + 1 CNC)")
	cmd.Flags().StringVar(&connectivity, "connectivity", "PodNetwork,ServiceNetwork", "Comma-separated connectivity types for ClusterNetworkConnect")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 2*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().DurationVar(&jobPause, "job-pause", 1*time.Minute, "Pause after CNC creation to allow OVN-K network settling")
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Enable pprof collection for ovnkube components")
	cmd.Flags().DurationVar(&pprofInterval, "pprof-interval", 0, "Interval between pprof collections")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
