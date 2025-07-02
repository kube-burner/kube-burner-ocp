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
	"github.com/spf13/cobra"
)

// NewClusterDensity holds cluster-density workload
func NewWebBurner(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var limitcount, scale int
	var bfd, crd, icni, probe, sriov bool
	var bridge string
	var podReadyThreshold time.Duration
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			additionalVars := map[string]any{
				"BFD":                 bfd,
				"BRIDGE":              bridge,
				"CRD":                 crd,
				"ICNI":                icni,
				"LIMITCOUNT":          limitcount,
				"POD_READY_THRESHOLD": podReadyThreshold,
				"PROBE":               probe,
				"SCALE":               scale,
				"SRIOV":               sriov,
			}
			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", additionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 2*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().IntVar(&limitcount, "limitcount", 1, "Limitcount")
	cmd.Flags().IntVar(&scale, "scale", 1, "Scale")
	cmd.Flags().BoolVar(&bfd, "bfd", true, "Enable BFD")
	cmd.Flags().BoolVar(&crd, "crd", true, "Enable AdminPolicyBasedExternalRoute CR")
	cmd.Flags().BoolVar(&icni, "icni", true, "Enable ICNI functionality")
	cmd.Flags().BoolVar(&probe, "probe", false, "Enable readiness probes")
	cmd.Flags().BoolVar(&sriov, "sriov", true, "Enable SRIOV")
	cmd.Flags().StringVar(&bridge, "bridge", "br-ex", "Data-plane bridge")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
