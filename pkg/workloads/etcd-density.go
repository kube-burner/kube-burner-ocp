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

	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"github.com/spf13/cobra"
)

func NewEtcdDensity(wh *workloads.WorkloadHelper) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "etcd-density",
		Short: "Etcd density/pressure workloads for etcd sharding validation",
	}
	cmd.AddCommand(
		newEventStorm(wh),
		newCrashloopFlood(wh),
		newDBQuotaPressure(wh),
		newAnnotationChurn(wh),
	)
	return cmd
}

func newEventStorm(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations int
	var metricsProfiles []string
	var rc int
	var podReadyThreshold time.Duration
	var deletionStrategy string
	var eventIterations, eventReplicas, stormWorkers int
	var podReplicas, stormDelay int

	cmd := &cobra.Command{
		Use:          "event-storm",
		Short:        "Synthetic event storm with concurrent victim workload",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["EVENT_ITERATIONS"] = eventIterations
			AdditionalVars["EVENT_REPLICAS"] = eventReplicas
			AdditionalVars["STORM_WORKERS"] = stormWorkers
			AdditionalVars["STORM_DELAY"] = stormDelay
			AdditionalVars["POD_REPLICAS"] = podReplicas
			rc = RunWorkload(cmd, wh, "event-storm.yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 100, "Victim workload iterations")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 0, "Pod ready timeout threshold")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", "default", "Deletion strategy")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"etcd-density-metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().IntVar(&eventIterations, "event-iterations", 2000, "Event creation iterations for the background storm")
	cmd.Flags().IntVar(&eventReplicas, "event-replicas", 100, "Event replicas per iteration")
	cmd.Flags().IntVar(&stormWorkers, "storm-workers", 20, "Parallel workers for event creation")
	cmd.Flags().IntVar(&stormDelay, "storm-delay", 15, "Seconds to let the storm build pressure before starting the victim workload")
	cmd.Flags().IntVar(&podReplicas, "pod-replicas", 3, "Pod replicas per victim deployment")
	return cmd
}

func newCrashloopFlood(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations int
	var metricsProfiles []string
	var rc int
	var podReadyThreshold time.Duration
	var deletionStrategy string
	var crashloopReplicas int
	var soakDuration time.Duration

	cmd := &cobra.Command{
		Use:          "crashloop-flood",
		Short:        "Crashlooping pods generating continuous event pressure",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["CRASHLOOP_REPLICAS"] = crashloopReplicas
			AdditionalVars["SOAK_DURATION"] = soakDuration
			rc = RunWorkload(cmd, wh, "crashloop-flood.yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 20, "Number of namespaces to create crashlooping pods in")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 0, "Pod ready timeout threshold")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", "default", "Deletion strategy")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"etcd-density-metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().IntVar(&crashloopReplicas, "crashloop-replicas", 100, "Crashlooping pods per iteration")
	cmd.Flags().DurationVar(&soakDuration, "soak-duration", 30*time.Minute, "Soak duration for event accumulation after pod creation")
	return cmd
}

func newDBQuotaPressure(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations, iterationsPerNamespace int
	var metricsProfiles []string
	var rc int
	var kbChunks int
	var kbSize int

	cmd := &cobra.Command{
		Use:          "db-quota-pressure",
		Short:        "Large CRD object fill to stress etcd storage quotas",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["ITERATIONS_PER_NAMESPACE"] = iterationsPerNamespace
			AdditionalVars["KB_CHUNKS"] = kbChunks
			AdditionalVars["KB_SIZE"] = kbSize
			rc = RunWorkload(cmd, wh, "db-quota-pressure.yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 6551, "Number of object iterations for the db-quota-pressure job")
	cmd.Flags().IntVar(&iterationsPerNamespace, "iterations-per-namespace", 936, "Number of iterations per namespace")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"etcd-density-metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().IntVar(&kbChunks, "kb-chunks", 8, "Number of CRDs to create and object replicas per iteration")
	cmd.Flags().IntVar(&kbSize, "kb-size", 100, "Size of each object in KB (10 or 100)")
	return cmd
}

func newAnnotationChurn(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations int
	var metricsProfiles []string
	var rc int
	var deletionStrategy string
	var churnReplicas, churnRounds int

	cmd := &cobra.Command{
		Use:          "annotation-churn",
		Short:        "Rapid annotation patching to create etcd revision bloat",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["CHURN_REPLICAS"] = churnReplicas
			AdditionalVars["CHURN_ROUNDS"] = churnRounds
			rc = RunWorkload(cmd, wh, "annotation-churn.yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 10, "Target creation iterations (one namespace each)")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", "default", "Deletion strategy")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"etcd-density-metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().IntVar(&churnReplicas, "churn-replicas", 50, "Target ConfigMaps per iteration")
	cmd.Flags().IntVar(&churnRounds, "churn-rounds", 10, "Number of patch rounds across all targets")
	return cmd
}
