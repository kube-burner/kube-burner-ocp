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

package workloads

import (
	"os"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"

	"github.com/spf13/cobra"
)

// Returns virt-density workload
func NewVirtDensity(wh *workloads.WorkloadHelper) *cobra.Command {
	var vmImage, deletionStrategy, churnMode string
	var vmsPerNode, iterationsPerNamespace, churnPercent, churnCycles int
	var vmiRunningThreshold time.Duration
	var namespacedIterations bool
	var churnDelay, churnDuration time.Duration
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "virt-density",
		Short:        "Runs virt-density workload",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			totalVMs := clusterMetadata.WorkerNodesCount * vmsPerNode
			vmCount, err := wh.MetadataAgent.GetCurrentVMICount()

			if err != nil {
				log.Fatal(err.Error())
			}
			AdditionalVars["JOB_ITERATIONS"] = totalVMs - vmCount
			AdditionalVars["VMI_RUNNING_THRESHOLD"] = vmiRunningThreshold
			AdditionalVars["VM_IMAGE"] = vmImage
			AdditionalVars["NAMESPACED_ITERATIONS"] = namespacedIterations
			AdditionalVars["ITERATIONS_PER_NAMESPACE"] = iterationsPerNamespace
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["CHURN_MODE"] = churnMode
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			setMetrics(cmd, metricsProfiles)
			AddVirtMetadata(wh, vmImage, "", "")

			wh.SetVariables(AdditionalVars, nil)
			rc = wh.Run(cmd.Name() + ".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&vmsPerNode, "vms-per-node", 245, "VMs per node")
	cmd.Flags().DurationVar(&vmiRunningThreshold, "vmi-ready-threshold", 25*time.Second, "VMI ready timeout threshold")
	cmd.Flags().BoolVar(&namespacedIterations, "namespaced-iterations", false, "Namespaced iterations")
	cmd.Flags().IntVar(&iterationsPerNamespace, "iterations-per-namespace", 10, "Iterations per namespace")
	cmd.Flags().StringVar(&vmImage, "vm-image", "quay.io/openshift-cnv/qe-cnv-tests-fedora:40", "Vm Image to be deployed")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.GVRDeletionStrategy, "GC deletion mode, default deletes entire namespaces and gvr deletes objects within namespaces before deleting the parent namespace")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 0, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&churnMode, "churn-mode", string(config.ChurnObjects), "Either namespaces, to churn entire namespaces or objects, to churn individual objects")
	return cmd
}
