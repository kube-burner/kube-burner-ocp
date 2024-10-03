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

package ocp

import (
	"fmt"
	"os"
	"time"

	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"

	"github.com/spf13/cobra"
)

// NewNodeDensity holds node-density workload
func NewNodeDensity(wh *workloads.WorkloadHelper) *cobra.Command {
	var podsPerNode int
	var podReadyThreshold time.Duration
	var containerImage string
	var metricsProfiles []string
	cmd := &cobra.Command{
		Use:          "node-density",
		Short:        "Runs node-density workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			totalPods := clusterMetadata.WorkerNodesCount * podsPerNode
			podCount, err := wh.MetadataAgent.GetCurrentPodCount()
			if err != nil {
				log.Fatal(err.Error())
			}
			os.Setenv("JOB_ITERATIONS", fmt.Sprint(totalPods-podCount))
			os.Setenv("POD_READY_THRESHOLD", fmt.Sprintf("%v", podReadyThreshold))
			os.Setenv("CONTAINER_IMAGE", containerImage)
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			wh.Run(cmd.Name())
		},
	}
	cmd.Flags().IntVar(&podsPerNode, "pods-per-node", 245, "Pods per node")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 15*time.Second, "Pod ready timeout threshold")
	cmd.Flags().StringVar(&containerImage, "container-image", "gcr.io/google_containers/pause:3.1", "Container image")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
