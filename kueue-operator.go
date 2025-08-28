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
	"time"

	"github.com/kube-burner/kube-burner/pkg/workloads"

	"github.com/spf13/cobra"
)

// NewKueueOperator holds kueue-operator workload
func NewKueueOperator(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var rc int
	var metricsProfiles []string
	var iterations int
	var podReadyThreshold time.Duration
	var memoryQuota, workloadRuntime string
	var cpuQuota, podsQuota, parallelism int

	var defaultPodsQuota, defaultIterations int

	cmd := &cobra.Command{
		Use:          variant,
		Short:        fmt.Sprintf("Runs %v workload", variant),
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			AdditionalVars["CPU_QUOTA"] = cpuQuota
			AdditionalVars["MEMORY_QUOTA"] = memoryQuota
			AdditionalVars["PODS_QUOTA"] = podsQuota
			AdditionalVars["PARALLELISM"] = parallelism
			AdditionalVars["ITERATIONS"] = iterations
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["WORKLOAD_RUNTIME"] = workloadRuntime
			setMetrics(cmd, metricsProfiles)
			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	switch variant {
	case "kueue-operator-jobs-shared":
		defaultPodsQuota = 400
		defaultIterations = 10
	default:
		defaultPodsQuota = 2000
		defaultIterations = 1
	}

	cmd.Flags().IntVar(&iterations, "iterations", defaultIterations, "Number of iterations/namespaces")
	cmd.Flags().IntVar(&cpuQuota, "cpu-quota", 150, "CPU quota per Kueue")
	cmd.Flags().StringVar(&memoryQuota, "memory-quota", "480Gi", "Memory quota per Kueue")
	cmd.Flags().IntVar(&podsQuota, "pods-quota", defaultPodsQuota, "Pods quota per Kueue")
	cmd.Flags().IntVar(&parallelism, "parallelism", 5, "Number of jobs or pods to run in parallel")
	cmd.Flags().StringVar(&workloadRuntime, "workload-runtime", "10s", "Workload runtime")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 2*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
