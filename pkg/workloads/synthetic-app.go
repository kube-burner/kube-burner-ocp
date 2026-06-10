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

	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"github.com/spf13/cobra"
)

// NewSyntheticApp creates the synthetic-app workload command
// This workload simulates realistic multi-tier applications to stress OVN/OVS
// control plane and data plane simultaneously for network performance analysis
func NewSyntheticApp(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations int
	var fillerDeployments, fillerServices, networkPolicies int
	var usersPerFrontend, frontendThreads, dbConnections int
	var trafficDuration time.Duration
	var metricsReportInterval time.Duration
	var churn bool
	var churnDeployments, churnNetworkPolicies int
	var podReadyThreshold time.Duration
	var metricsProfiles []string
	var uperfImage, metricsStoreImage string
	var rc int

	cmd := &cobra.Command{
		Use:   "synthetic-app",
		Short: "Runs synthetic-app workload to simulate multi-tier applications for OVN/OVS performance analysis",
		Long: `The synthetic-app workload simulates realistic multi-tier customer applications to stress
both OVN/OVS control plane and data plane simultaneously. It runs three independent uperf
traffic patterns:

  1. Users → Frontend: Many concurrent connections simulating user load
  2. Frontend → Microservices: Fan-out pattern to multiple backend services
  3. App → Database: OLTP-style request/response traffic

Each namespace contains:
  - Traffic pattern client/server deployments
  - Filler deployments to create realistic resource density
  - Services and NetworkPolicies to stress OVN NBDB/SBDB

Metrics are collected via a DaemonSet metrics-store that receives periodic pushes
from uperf clients and aggregates them for kube-burner indexing.`,
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)

			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["FILLER_DEPLOYMENTS"] = fillerDeployments
			AdditionalVars["FILLER_SERVICES"] = fillerServices
			AdditionalVars["NETWORK_POLICIES"] = networkPolicies
			AdditionalVars["USERS_PER_FRONTEND"] = usersPerFrontend
			AdditionalVars["FRONTEND_THREADS"] = frontendThreads
			AdditionalVars["DB_CONNECTIONS"] = dbConnections
			AdditionalVars["TRAFFIC_DURATION"] = trafficDuration.String()
			AdditionalVars["METRICS_REPORT_INTERVAL"] = int(metricsReportInterval.Seconds())
			AdditionalVars["CHURN"] = churn
			AdditionalVars["CHURN_DEPLOYMENTS"] = churnDeployments
			AdditionalVars["CHURN_NETWORK_POLICIES"] = churnNetworkPolicies
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["UPERF_IMAGE"] = uperfImage
			AdditionalVars["METRICS_STORE_IMAGE"] = metricsStoreImage

			rc = RunWorkload(cmd, wh, "synthetic-app.yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}

	// Namespace/iteration configuration
	cmd.Flags().IntVar(&iterations, "iterations", 1, "Number of synthetic app namespaces to create")

	// Resource density configuration (filler resources for control plane load)
	cmd.Flags().IntVar(&fillerDeployments, "filler-deployments", 1, "Number of filler deployments per namespace (adds to 70 total with traffic patterns)")
	cmd.Flags().IntVar(&fillerServices, "filler-services", 1, "Number of filler services per namespace (adds to 50 total)")
	cmd.Flags().IntVar(&networkPolicies, "network-policies", 5, "Number of network policies per namespace")

	// Traffic pattern configuration
	cmd.Flags().IntVar(&usersPerFrontend, "users-per-frontend", 100, "Number of concurrent user connections to frontend (Pattern 1 threads)")
	cmd.Flags().IntVar(&frontendThreads, "frontend-threads", 50, "Number of frontend handler threads for microservice fan-out (Pattern 2)")
	cmd.Flags().IntVar(&dbConnections, "db-connections", 200, "Number of database connection pool size (Pattern 3 threads)")
	cmd.Flags().DurationVar(&trafficDuration, "traffic-duration", 5*time.Minute, "Duration to run uperf traffic patterns")
	cmd.Flags().DurationVar(&metricsReportInterval, "metrics-report-interval", 30*time.Second, "Interval for uperf clients to report metrics to metrics-store")

	// Churn configuration (control plane stress)
	cmd.Flags().BoolVar(&churn, "churn", false, "Enable control plane churn during traffic (create additional resources)")
	cmd.Flags().IntVar(&churnDeployments, "churn-deployments", 20, "Number of deployments to create during churn")
	cmd.Flags().IntVar(&churnNetworkPolicies, "churn-network-policies", 50, "Number of network policies to create during churn")

	// Pod configuration
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 2*time.Minute, "Pod ready timeout threshold")

	// Image configuration
	cmd.Flags().StringVar(&uperfImage, "uperf-image", "quay.io/cloud-bulldozer/uperf:latest", "uperf container image")
	cmd.Flags().StringVar(&metricsStoreImage, "metrics-store-image", "quay.io/cloud-bulldozer/synthetic-app-metrics-store:latest", "Metrics store container image")

	// Metrics configuration
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")

	cmd.MarkFlagRequired("iterations")

	return cmd
}
