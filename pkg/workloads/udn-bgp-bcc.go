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
	"context"
	"fmt"
	"os"
	"time"

	kubeburnermeasurements "github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kube-burner/kube-burner-ocp/pkg/measurements"
	"github.com/kube-burner/kube-burner-ocp/pkg/utils"
)

// bccCleanupSelector selects the resources cleanupBCCResources deletes: only those created by this
// workload's jobs (see udn-bgp-bcc.yml), so that other kube-burner workloads' leftovers are kept.
const bccCleanupSelector = "kube-burner.io/job in (udn-bgp-bcc-create-namespaces,udn-bgp-bcc-create-bgp-routing)"

// cleanupBCCResources deletes BGP Cloud Connector resources in the correct order:
// 1. BGPRouting CRs (automatically cleans up their managed CUDNs via finalizers and withdraws routes)
// 2. Namespaces (cascade deletes pods and other namespaced resources)
func cleanupBCCResources(ctx context.Context, labelSelector string) {
	log.Infof("Cleaning up BGPRouting CRs with label: %s", labelSelector)
	bgpRoutings := getK8SConnector().DynamicClient().Resource(measurements.BGPRoutingGVR)
	if err := bgpRoutings.DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: labelSelector}); err != nil {
		log.Warnf("Error deleting BGPRouting CRs: %v", err)
	}

	// Wait for BGPRouting finalizers to clean up CUDNs and for BGP routes to be withdrawn
	time.Sleep(10 * time.Second)

	log.Infof("Cleaning up namespaces with label: %s", labelSelector)
	cleanupTestNamespaces(ctx, labelSelector)
}

// NewUdnBgpBCC creates the BGP Cloud Connector variant of the udn-bgp workload.
// It supports AWS VPC Route Server and Azure Route Server.
func NewUdnBgpBCC(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var (
		iterations      int
		metricsProfiles []string
		cloudProvider   string

		// AWS-specific settings
		awsRegion        string
		awsRouteServerID string
		awsVpcID         string

		// Azure-specific settings
		azureSubscriptionID  string
		azureResourceGroup   string
		azureRouteServerName string

		// Created and validated once in PreRunE, then shared with the measurement
		routeProvider utils.BCCRouteServerProvider

		rc int
	)
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			// Validate here rather than in the measurement: kube-burner only logs a measurement that fails
			// to start and runs the workload anyway, which would leave the run without latency data.
			// Everything is validated before the cleanup, so a configuration error deletes nothing.
			// Each case assigns routeProvider only on success: assigning a constructor's nil *Provider
			// to the interface directly would make routeProvider non-nil.
			switch cloudProvider {
			case "aws":
				if awsRouteServerID == "" || awsRegion == "" || awsVpcID == "" {
					return fmt.Errorf("--aws-route-server-id, --aws-region, and --aws-vpc-id are required")
				}
				provider, err := utils.NewAWSRouteServerProvider(awsRegion, awsRouteServerID, awsVpcID)
				if err != nil {
					return fmt.Errorf("invalid AWS configuration (check credentials, aws-region, aws-route-server-id, and aws-vpc-id): %w", err)
				}
				routeProvider = provider
			case "azure":
				if azureSubscriptionID == "" || azureResourceGroup == "" || azureRouteServerName == "" {
					return fmt.Errorf("--azure-subscription-id, --azure-resource-group, and --azure-route-server-name are required")
				}
				provider, err := utils.NewAzureRouteServerProvider(azureSubscriptionID, azureResourceGroup, azureRouteServerName)
				if err != nil {
					return fmt.Errorf("invalid Azure configuration (check credentials, subscription, resource group, and route server name): %w", err)
				}
				routeProvider = provider
			default:
				return fmt.Errorf("unsupported cloud provider: %s (must be aws or azure)", cloudProvider)
			}
			if err := utils.ValidateConnectivity(cloudProvider, routeProvider); err != nil {
				return err
			}

			// Clean up resources from previous runs in the correct order. The measurement resets the
			// provider's route sightings, so routes still being withdrawn here do not affect it.
			cleanupBCCResources(context.Background(), bccCleanupSelector)
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["JOB_ITERATIONS"] = iterations

			wh.SetMeasurements(map[string]kubeburnermeasurements.NewMeasurementFactory{
				"raLatencyBCC": measurements.NewRaLatencyBCCMeasurementFactory(routeProvider),
			})
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			// Clean up resources if GC is enabled
			if SetVars["GC"] == "true" {
				cleanupBCCResources(context.Background(), bccCleanupSelector)
			}
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 10, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().StringVar(&cloudProvider, "cloud-provider", "aws", "Cloud provider: aws or azure")

	// AWS-specific flags
	cmd.Flags().StringVar(&awsRegion, "aws-region", "", "[AWS] AWS region containing the Route Server")
	cmd.Flags().StringVar(&awsRouteServerID, "aws-route-server-id", "", "[AWS] AWS Route Server ID")
	cmd.Flags().StringVar(&awsVpcID, "aws-vpc-id", "", "[AWS] AWS VPC ID for route table verification (T3/T3.5 measurement)")

	// Azure-specific flags
	cmd.Flags().StringVar(&azureSubscriptionID, "azure-subscription-id", "", "[Azure] Azure subscription ID")
	cmd.Flags().StringVar(&azureResourceGroup, "azure-resource-group", "", "[Azure] Resource group of the Azure Route Server (not the ARO cluster resource group)")
	cmd.Flags().StringVar(&azureRouteServerName, "azure-route-server-name", "", "[Azure] Azure Route Server name")
	return cmd
}
