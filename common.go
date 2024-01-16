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

package ocp

import (
	"context"
	"fmt"
	"time"

	ocpmetadata "github.com/cloud-bulldozer/go-commons/ocp-metadata"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	"github.com/openshift/client-go/config/clientset/versioned"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

const clusterMetadataMetric = "clusterMetadata"

func getMetrics(cmd *cobra.Command, metricsProfile string) []string {
	var metricsProfiles []string
	profileType, _ := cmd.Root().PersistentFlags().GetString("profile-type")
	switch ProfileType(profileType) {
	case Reporting:
		metricsProfiles = []string{"metrics-report.yml"}
	case Regular:
		metricsProfiles = []string{metricsProfile}
	case Both:
		metricsProfiles = []string{"metrics-report.yml", metricsProfile}
	}
	return metricsProfiles
}

// Verifies container registry and reports its status
func verifyContainerRegistry(restConfig *rest.Config) bool {
	// Create an OpenShift client using the default configuration
	client, err := versioned.NewForConfig(restConfig)
	if err != nil {
		log.Error("Error connecting to the openshift cluster", err)
		return false
	}

	// Get the image registry object
	imageRegistry, err := client.ConfigV1().ClusterOperators().Get(context.TODO(), "image-registry", metav1.GetOptions{})
	if err != nil {
		log.Error("Error getting image registry object:", err)
		return false
	}

	// Check the status conditions
	logMessage := ""
	readyFlag := false
	for _, condition := range imageRegistry.Status.Conditions {
		if condition.Type == "Available" && condition.Status == "True" {
			readyFlag = true
			logMessage += " up and running"
		}
		if condition.Type == "Progressing" && condition.Status == "False" && condition.Reason == "Ready" {
			logMessage += " ready to use"
		}
		if condition.Type == "Degraded" && condition.Status == "False" && condition.Reason == "AsExpected" {
			logMessage += " with a healthy state"
		}
	}
	if readyFlag {
		log.Infof("Cluster image registry is%s", logMessage)
	} else {
		log.Info("Cluster image registry is not up and running")
	}
	return readyFlag
}

// SetKubeBurnerFlags configures the required environment variables and flags for kube-burner
func GatherMetadata(wh *workloads.WorkloadHelper, alerting bool) error {
	var err error
	wh.MetadataAgent, err = ocpmetadata.NewMetadata(wh.RestConfig)
	if err != nil {
		return err
	}
	// When either indexing or alerting are enabled
	if alerting && wh.MetricsEndpoint == "" {
		wh.PrometheusURL, wh.PrometheusToken, err = wh.MetadataAgent.GetPrometheus()
		if err != nil {
			return fmt.Errorf("error obtaining Prometheus information: %v", err)
		}
	}
	wh.Metadata.ClusterMetadata, err = wh.MetadataAgent.GetClusterMetadata()
	if err != nil {
		return err
	}
	wh.Metadata.UUID = wh.UUID
	wh.Metadata.Timestamp = time.Now().UTC()
	wh.Metadata.MetricName = clusterMetadataMetric
	wh.MetricsMetadata = map[string]interface{}{
		"platform":        wh.Metadata.Platform,
		"ocpVersion":      wh.Metadata.OCPVersion,
		"ocpMajorVersion": wh.Metadata.OCPMajorVersion,
		"k8sVersion":      wh.Metadata.K8SVersion,
		"totalNodes":      wh.Metadata.TotalNodes,
		"sdnType":         wh.Metadata.SDNType,
	}
	return nil
}
