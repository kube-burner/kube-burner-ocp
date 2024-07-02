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

	"github.com/kube-burner/kube-burner/pkg/workloads"
	"github.com/spf13/cobra"
)

// NewCrdScale holds the crd-scale workload
func NewCrdScale(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations int
	cmd := &cobra.Command{
		Use:          "crd-scale",
		Short:        "Runs crd-scale workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			os.Setenv("JOB_ITERATIONS", fmt.Sprint(iterations))
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, "metrics-aggregated.yml")
			wh.Run(cmd.Name())
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Number of CRDs to create")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
