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

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"github.com/spf13/cobra"
)

// NewBerserkerLoad holds berserker-load workload
func NewBerserkerLoad(wh *workloads.WorkloadHelper) *cobra.Command {
	var rc int
	var metricsProfiles []string
	var jobIterations int
	var processLoadReplicas, endpointLoadReplicas, connectionLoadReplicas int
	var serviceReplicas, secretReplicas, configmapReplicas int
	var churnCycles, churnPercent int
	var churnDuration, churnDelay, jobPause, maxWaitTimeout, podReadyThreshold time.Duration
	var deletionStrategy, churnMode string
	var processLoadImage, endpointLoadImage, connectionLoadImage string
	var dockerConfigJson string

	cmd := &cobra.Command{
		Use:          "berserker-load",
		Short:        "Runs berserker-load workload for StackRox/RHACS performance testing",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			// Set template variables
			AdditionalVars["JOB_ITERATIONS"] = jobIterations
			AdditionalVars["JOB_PAUSE"] = jobPause
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["CHURN_MODE"] = churnMode
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["MAX_WAIT_TIMEOUT"] = maxWaitTimeout
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold

			// Replica counts
			AdditionalVars["PROCESS_LOAD_REPLICAS"] = processLoadReplicas
			AdditionalVars["ENDPOINT_LOAD_REPLICAS"] = endpointLoadReplicas
			AdditionalVars["CONNECTION_LOAD_REPLICAS"] = connectionLoadReplicas
			AdditionalVars["SERVICE_REPLICAS"] = serviceReplicas
			AdditionalVars["SECRET_REPLICAS"] = secretReplicas
			AdditionalVars["CONFIGMAP_REPLICAS"] = configmapReplicas

			// Container images
			AdditionalVars["PROCESS_LOAD_IMAGE"] = processLoadImage
			AdditionalVars["ENDPOINT_LOAD_IMAGE"] = endpointLoadImage
			AdditionalVars["CONNECTION_LOAD_IMAGE"] = connectionLoadImage

			// Docker config for image pull secret
			AdditionalVars["DOCKER_CONFIG_JSON"] = dockerConfigJson

			setMetrics(cmd, metricsProfiles)
			rc = RunWorkload(cmd, wh, "berserker-load.yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}

	// Job configuration flags
	cmd.Flags().IntVar(&jobIterations, "job-iterations", 5, "Number of namespaces to create")
	cmd.Flags().DurationVar(&jobPause, "job-pause", 1000*time.Hour, "Duration to pause after creating resources")
	cmd.Flags().DurationVar(&maxWaitTimeout, "max-wait-timeout", 12*time.Minute, "Maximum wait timeout for pod ready")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 5*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.GVRDeletionStrategy, "GC deletion mode")

	// Churn configuration flags
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Number of churn cycles (0 = infinite when churn-duration > 0)")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 1000*time.Hour, "Churn duration (0 disables churn)")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 10*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 80, "Percentage of job iterations to churn each round")
	cmd.Flags().StringVar(&churnMode, "churn-mode", string(config.ChurnObjects), "Churn mode: namespaces or objects")

	// Replica count flags
	cmd.Flags().IntVar(&processLoadReplicas, "process-load-replicas", 6, "Number of process-load DaemonSets")
	cmd.Flags().IntVar(&endpointLoadReplicas, "endpoint-load-replicas", 6, "Number of endpoint-load DaemonSets")
	cmd.Flags().IntVar(&connectionLoadReplicas, "connection-load-replicas", 6, "Number of connection-load DaemonSets")
	cmd.Flags().IntVar(&serviceReplicas, "service-replicas", 10, "Number of services")
	cmd.Flags().IntVar(&secretReplicas, "secret-replicas", 10, "Number of image pull secrets")
	cmd.Flags().IntVar(&configmapReplicas, "configmap-replicas", 10, "Number of ConfigMaps")

	// Container image flags
	cmd.Flags().StringVar(&processLoadImage, "process-load-image",
		"quay.io/rhacs-eng/qa:berserker-1.0-63-g7b0a20bf5f", "Process load container image")
	cmd.Flags().StringVar(&endpointLoadImage, "endpoint-load-image",
		"quay.io/rhacs-eng/qa:berserker-1.0-40-ge3bd96aa5a", "Endpoint load container image")
	cmd.Flags().StringVar(&connectionLoadImage, "connection-load-image",
		"quay.io/rhacs-eng/qa:berserker-network-1.0-85-g1b7ab034aa", "Connection load container image")

	// Image pull secret configuration
	cmd.Flags().StringVar(&dockerConfigJson, "docker-config-json", "",
		"Docker config JSON for image pull secret (base64 encoded or plain JSON)")

	// Metrics profile
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"},
		"Comma separated list of metrics profiles to use")

	return cmd
}
