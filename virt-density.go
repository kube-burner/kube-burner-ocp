// Copyright 2024 The Kube-burner Authors.
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

// Returns virt-density workload
func NewVirtDensity(wh *workloads.WorkloadHelper) *cobra.Command {
	var vmsPerNode int
	var vmiRunningThreshold time.Duration
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "virt-density",
		Short:        "Runs virt-density workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			totalVMs := clusterMetadata.WorkerNodesCount * vmsPerNode
			vmCount, err := wh.MetadataAgent.GetCurrentVMICount()
			if err != nil {
				log.Fatal(err.Error())
			}
			os.Setenv("JOB_ITERATIONS", fmt.Sprint(totalVMs-vmCount))
			os.Setenv("VMI_RUNNING_THRESHOLD", fmt.Sprintf("%v", vmiRunningThreshold))
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			rc = wh.Run(cmd.Name())
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&vmsPerNode, "vms-per-node", 245, "VMs per node")
	cmd.Flags().DurationVar(&vmiRunningThreshold, "vmi-ready-threshold", 20*time.Second, "VMI ready timeout threshold")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
