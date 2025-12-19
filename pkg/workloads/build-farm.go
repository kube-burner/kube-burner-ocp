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
	"fmt"
	"os"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"

	"github.com/spf13/cobra"
)

// NewBuildFarm holds build-farm workload
func NewBuildFarm(wh *workloads.WorkloadHelper) *cobra.Command {
	var rc int
	var metricsProfiles []string
	var jobIterations, iterationsPerNamespace int
	var churnCycles, churnPercent int
	var qps, burst float64
	var churnDuration, churnDelay time.Duration
	var deletionStrategy string
	var namespacedIterations, churn, pprof bool

	// Controller configuration
	var numControllers, numThreads int
	var enableJobWatcher, enablePodWatcher, enableJobsListing, enableSecretsListing bool
	var watcherRestartInterval, sleepBeforeRestart, secretsListInterval, jobsListInterval time.Duration
	var namespaceFilterRegEx string

	// Build job configuration
	var metadataIterations int
	var metadataIterationsDelay time.Duration
	var numWatchers int
	var buildImage, buildPlatform, targetImage string
	var smallJobPercent int

	cmd := &cobra.Command{
		Use:          "build-farm",
		Short:        "Runs build-farm workload",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			// Validate small job percentage
			if smallJobPercent < 0 || smallJobPercent > 100 {
				log.Fatalf("small-job-percent must be between 0 and 100, got %d", smallJobPercent)
			}
			largeJobPercent := 100 - smallJobPercent

			// Set standard variables
			AdditionalVars["JOB_ITERATIONS"] = jobIterations
			AdditionalVars["ITERATIONS_PER_NAMESPACE"] = iterationsPerNamespace
			AdditionalVars["NAMESPACED_ITERATIONS"] = namespacedIterations
			AdditionalVars["QPS"] = qps
			AdditionalVars["BURST"] = burst
			AdditionalVars["PPROF"] = pprof

			// Set churn configuration
			AdditionalVars["CHURN"] = churn
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy

			// Set controller configuration
			AdditionalVars["NUM_CONTROLLERS"] = numControllers
			AdditionalVars["NUM_THREADS"] = fmt.Sprint(numThreads)
			AdditionalVars["ENABLE_JOB_WATCHER"] = enableJobWatcher
			AdditionalVars["ENABLE_POD_WATCHER"] = enablePodWatcher
			AdditionalVars["ENABLE_JOBS_LISTING"] = enableJobsListing
			AdditionalVars["ENABLE_SECRETS_LISTING"] = enableSecretsListing
			AdditionalVars["WATCHER_RESTART_INTERVAL"] = watcherRestartInterval.String()
			AdditionalVars["SLEEP_BEFORE_RESTART"] = sleepBeforeRestart.String()
			AdditionalVars["SECRETS_LIST_INTERVAL"] = secretsListInterval.String()
			AdditionalVars["JOBS_LIST_INTERVAL"] = jobsListInterval.String()
			AdditionalVars["NAMESPACE_FILTER_REGEX"] = namespaceFilterRegEx

			// Set build job configuration
			AdditionalVars["METADATA_ITERATIONS"] = metadataIterations
			AdditionalVars["METADATA_ITERATIONS_DELAY"] = metadataIterationsDelay.String()
			AdditionalVars["NUM_WATCHERS"] = numWatchers
			AdditionalVars["BUILD_IMAGE"] = buildImage
			AdditionalVars["BUILD_PLATFORM"] = buildPlatform
			AdditionalVars["TARGET_IMAGE"] = targetImage
			AdditionalVars["SMALL_JOB_PERCENT"] = smallJobPercent
			AdditionalVars["LARGE_JOB_PERCENT"] = largeJobPercent

			log.Infof("Running build-farm workload with %d job iterations across %d iterations per namespace", jobIterations, iterationsPerNamespace)
			if churn {
				log.Infof("Churn enabled: %d cycles, %d%% churn rate, %v delay", churnCycles, churnPercent, churnDelay)
			}
			log.Infof("Controller config: %d controllers, %d threads per controller", numControllers, numThreads)

			setMetrics(cmd, metricsProfiles)
			wh.SetVariables(AdditionalVars, nil)
			rc = wh.Run(cmd.Name() + ".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}

	// Standard workload flags
	cmd.Flags().IntVar(&jobIterations, "job-iterations", 100, "Number of job iterations to create")
	cmd.Flags().IntVar(&iterationsPerNamespace, "iterations-per-namespace", 100, "Number of iterations per namespace")
	cmd.Flags().Float64Var(&qps, "qps", 40, "QPS for client rate limiting")
	cmd.Flags().Float64Var(&burst, "burst", 40, "Burst for client rate limiting")

	// Churn flags
	cmd.Flags().BoolVar(&churn, "churn", true, "Enable churning")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 5, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 1*time.Hour, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 3*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 50, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.DefaultDeletionStrategy, "GC deletion mode, default deletes entire namespaces and gvr deletes objects within namespaces before deleting the parent namespace")

	// Controller configuration flags
	cmd.Flags().IntVar(&numControllers, "num-controllers", 3, "Number of controller replicas")
	cmd.Flags().IntVar(&numThreads, "num-threads", 6, "Number of threads per controller")
	cmd.Flags().BoolVar(&enableJobWatcher, "enable-job-watcher", true, "Enable job watcher")
	cmd.Flags().BoolVar(&enablePodWatcher, "enable-pod-watcher", true, "Enable pod watcher")
	cmd.Flags().BoolVar(&enableJobsListing, "enable-jobs-listing", true, "Enable jobs listing")
	cmd.Flags().BoolVar(&enableSecretsListing, "enable-secrets-listing", false, "Enable secrets listing")
	cmd.Flags().DurationVar(&watcherRestartInterval, "watcher-restart-interval", 600*time.Second, "Watcher restart interval")
	cmd.Flags().DurationVar(&sleepBeforeRestart, "sleep-before-restart", 60*time.Second, "Sleep duration before restarting watchers")
	cmd.Flags().DurationVar(&secretsListInterval, "secrets-list-interval", 5*time.Second, "Secrets list interval")
	cmd.Flags().DurationVar(&jobsListInterval, "jobs-list-interval", 5*time.Second, "Jobs list interval")
	cmd.Flags().StringVar(&namespaceFilterRegEx, "namespace-filter-regex", "", "Namespace filter regex")

	// Build job configuration flags
	cmd.Flags().IntVar(&metadataIterations, "metadata-iterations", 2, "Number of metadata update iterations")
	cmd.Flags().DurationVar(&metadataIterationsDelay, "metadata-iterations-delay", 5*time.Second, "Delay between metadata iterations")
	cmd.Flags().IntVar(&numWatchers, "num-watchers", 32, "Number of watchers for label distribution")
	cmd.Flags().StringVar(&buildImage, "build-image", "quay.io/prometheus/busybox", "Container image to use for build simulation")
	cmd.Flags().StringVar(&buildPlatform, "build-platform", "linux/x86_64", "Build platform architecture")
	cmd.Flags().StringVar(&targetImage, "target-image", "registry.local/build-farm/test-image", "Target image registry path")
	cmd.Flags().IntVar(&smallJobPercent, "small-job-percent", 80, "Percentage of small build jobs (0-100, large jobs get remainder)")

	// Metrics profile
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")

	return cmd
}
