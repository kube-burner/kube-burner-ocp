// Copyright 2023 The Kube-burner Authors.
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

	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"github.com/spf13/cobra"
)

// NewPVCDensity holds pvc-density workload
func NewPVCDensity(wh *workloads.WorkloadHelper) *cobra.Command {

	var iterations int
	var metricsProfiles []string
	var claimSize string
	var containerImage, storageClassName string
	var rc int

	cmd := &cobra.Command{
		Use:          "pvc-density",
		Short:        "Runs pvc-density workload",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["CONTAINER_IMAGE"] = containerImage
			AdditionalVars["CLAIM_SIZE"] = claimSize
			AdditionalVars["STORAGE_CLASS_NAME"] = storageClassName

			setMetrics(cmd, metricsProfiles)
			AddWorkloadFlagsToMetadata(cmd, wh)
			wh.SetVariables(AdditionalVars, SetVars)
			rc = wh.Run(cmd.Name() + ".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVar(&storageClassName, "storage-class-name", "", "Storage class name, leave this empty to use the default one")
	cmd.Flags().IntVar(&iterations, "iterations", 0, fmt.Sprintf("%v iterations", iterations))
	cmd.Flags().StringVar(&claimSize, "claim-size", "256Mi", "claim-size=256Mi")
	cmd.Flags().StringVar(&containerImage, "container-image", "gcr.io/google_containers/pause:3.1", "Container image")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
