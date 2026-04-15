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
	"net"
	"os"
	"time"

	kubeburnermeasurements "github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/kube-burner/kube-burner-ocp/pkg/measurements"
)

var additionalMeasurementFactoryMap = map[string]kubeburnermeasurements.NewMeasurementFactory{
	"raLatency": measurements.NewRaLatencyMeasurementFactory,
}

// validateFrrExternalIP validates the external FRR router IP address format and connectivity
func validateFrrExternalIP(frrExternalIP string) error {
	if ip := net.ParseIP(frrExternalIP); ip == nil {
		return fmt.Errorf("invalid IP address format: %s", frrExternalIP)
	}

	log.Infof("Validating external FRR router connectivity at %s:179...", frrExternalIP)

	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:179", frrExternalIP), 5*time.Second)
	if err != nil {
		return fmt.Errorf("unable to connect to FRR at %s:179: %v. Please ensure the external FRR router is running and BGP is configured", frrExternalIP, err)
	}
	if conn != nil {
		conn.Close()
	}

	log.Infof("External FRR router at %s:179 is reachable.", frrExternalIP)
	return nil
}

// NewUdnBgp holds udn-bgp workload
func NewUdnBgp(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, namespacePerCudn int
	var enableVm bool
	var frrExternalIP string
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := validateFrrExternalIP(frrExternalIP); err != nil {
				return err
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["NAMESPACES_PER_CUDN"] = namespacePerCudn
			AdditionalVars["ENABLE_VM"] = enableVm
			wh.SetMeasurements(additionalMeasurementFactoryMap)
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 10, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().BoolVar(&enableVm, "vm", false, "Deploy a VM for the test instead of a pod")
	cmd.Flags().IntVar(&namespacePerCudn, "namespaces-per-cudn", 1, "Number of namespaces sharing the same cluster udn")
	cmd.Flags().StringVar(&frrExternalIP, "frr-external-ip", "", "IP address of the external FRR router (required)")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("iterations")
	cmd.MarkFlagRequired("frr-external-ip")
	return cmd
}
