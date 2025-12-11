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

	"github.com/kube-burner/kube-burner/pkg/workloads"

	"github.com/spf13/cobra"
)

const kueueOperatorJobsShared = "kueue-operator-jobs-shared"

// NewKueueOperator holds kueue-operator workload
func NewKueueOperator(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var rc int
	var metricsProfiles []string
	var iterations int
	var workloadRuntime string
	var defaultJobReplicas, podReplicas, jobReplicas, defaultIterations, parallelism int
	var QPS, burst int
	var podsQuota int
	var jobIterationDelay, namespaceDelay time.Duration
	cmd := &cobra.Command{
		Use:          variant,
		Short:        fmt.Sprintf("Runs %v workload", variant),
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			podsQuota = jobReplicas * parallelism
			if cmd.Name() == kueueOperatorJobsShared {
				// We set the pod quota to the 75% pods deployed in each iteration to ensure the clusterqueue share resources with others
				podsQuota = int(float64(jobReplicas*parallelism) * 0.75)
			}
			AdditionalVars["PODS_QUOTA"] = podsQuota
			AdditionalVars["JOB_REPLICAS"] = jobReplicas
			AdditionalVars["POD_REPLICAS"] = podReplicas
			AdditionalVars["PARALLELISM"] = parallelism
			AdditionalVars["ITERATIONS"] = iterations
			AdditionalVars["WORKLOAD_RUNTIME"] = workloadRuntime
			AdditionalVars["QPS"] = QPS
			AdditionalVars["BURST"] = burst
			AdditionalVars["JOB_ITERATION_DELAY"] = jobIterationDelay
			AdditionalVars["NAMESPACE_DELAY"] = namespaceDelay
			setMetrics(cmd, metricsProfiles)
			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}

	defaultJobReplicas = 2000
	defaultIterations = 1
	cmd.Flags().StringVar(&workloadRuntime, "workload-runtime", "10s", "Workload runtime")
	if variant == "kueue-operator-jobs" || variant == kueueOperatorJobsShared {
		cmd.Flags().IntVar(&parallelism, "parallelism", 5, "Number of jobs or pods to run in parallel")
		if variant == kueueOperatorJobsShared {
			defaultJobReplicas = 400
			defaultIterations = 10
		}
		cmd.Flags().IntVar(&jobReplicas, "job-replicas", defaultJobReplicas, "Jobs per iteration")
	}
	if variant == "kueue-operator-pods" {
		cmd.Flags().IntVar(&podReplicas, "pod-replicas", defaultJobReplicas, "Jobs per iteration")
	}
	cmd.Flags().IntVar(&iterations, "iterations", defaultIterations, "Number of iterations/namespaces")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"kueue-metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().DurationVar(&jobIterationDelay, "job-iteration-delay", 0, "Delay between job iterations")
	cmd.Flags().DurationVar(&namespaceDelay, "namespace-delay", 0, "Delay after completing all iterations in a namespace before starting the next namespace")
	cmd.PersistentFlags().IntVar(&QPS, "qps", 10, "QPS")
	cmd.PersistentFlags().IntVar(&burst, "burst", 10, "Burst")
	return cmd
}
