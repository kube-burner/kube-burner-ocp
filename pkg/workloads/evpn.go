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

	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// checkWebserverReachability validates that the external webserver is reachable
func checkWebserverReachability(ip, port string, timeout time.Duration) error {
	address := net.JoinHostPort(ip, port)
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return fmt.Errorf("external webserver at %s is not reachable: %w", address, err)
	}
	conn.Close()
	return nil
}

// NewEVPN holds evpn workload
func NewEVPN(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, namespacePerCudn int
	var metricsProfiles []string
	var rc int
	var scenario string
	var podReadyThreshold time.Duration
	var externalWebserverIP, externalWebserverPort string
	var connectionTimeout time.Duration
	var skipReachabilityCheck bool
	var createExtFrrVrf bool
	var l3vniStart int
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			// Validate scenario
			validScenarios := map[string]bool{
				"east-west":      true,
				"north-south":    true,
				"north-south-l3": true,
			}
			if !validScenarios[scenario] {
				return fmt.Errorf("unsupported scenario: %s. Valid scenarios are: east-west, north-south, north-south-l3", scenario)
			}

			// For north-south scenarios, validate required flags
			if scenario == "north-south" || scenario == "north-south-l3" {
				if externalWebserverIP == "" {
					return fmt.Errorf("--external-webserver-ip is required for %s scenario", scenario)
				}
				if externalWebserverPort == "" {
					return fmt.Errorf("--external-webserver-port is required for %s scenario", scenario)
				}

				// Check reachability unless skipped
				if !skipReachabilityCheck {
					log.Infof("Checking reachability of external webserver at %s:%s...", externalWebserverIP, externalWebserverPort)
					if err := checkWebserverReachability(externalWebserverIP, externalWebserverPort, connectionTimeout); err != nil {
						return err
					}
					log.Infof("External webserver is reachable")
				}
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["NAMESPACES_PER_CUDN"] = namespacePerCudn
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["SCENARIO"] = scenario

			// Set external webserver variables for north-south scenarios
			if scenario == "north-south" || scenario == "north-south-l3" {
				AdditionalVars["EXTERNAL_WEBSERVER_IP"] = externalWebserverIP
				AdditionalVars["EXTERNAL_WEBSERVER_PORT"] = externalWebserverPort
			}

			AdditionalVars["CREATE_EXT_FRR_VRF"] = createExtFrrVrf
			AdditionalVars["L3VNI_START"] = l3vniStart

			rc = RunWorkload(cmd, wh, "evpn.yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 2*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().IntVar(&iterations, "iterations", 10, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().IntVar(&namespacePerCudn, "namespaces-per-cudn", 1, "Number of namespaces sharing the same cluster udn")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().StringVar(&scenario, "scenario", "east-west", "Test scenario: east-west, north-south, or north-south-l3")
	cmd.Flags().StringVar(&externalWebserverIP, "external-webserver-ip", "", "External webserver IP for north-south scenarios (required for north-south and north-south-l3)")
	cmd.Flags().StringVar(&externalWebserverPort, "external-webserver-port", "", "External webserver port for north-south scenarios (required for north-south and north-south-l3)")
	cmd.Flags().DurationVar(&connectionTimeout, "connection-timeout", 10*time.Second, "Timeout for external webserver reachability check")
	cmd.Flags().BoolVar(&skipReachabilityCheck, "skip-reachability-check", true, "Skip the external webserver reachability check")
	cmd.Flags().BoolVar(&createExtFrrVrf, "create-ext-frr-vrf", false, "Setup external FRR VRF before workload and cleanup after completion")
	cmd.Flags().IntVar(&l3vniStart, "l3vni-start", 100, "Starting L3 VNI number for EVPN configuration")
	return cmd
}
