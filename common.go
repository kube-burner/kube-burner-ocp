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
	"fmt"
	"os"
	"strings"
	"time"

	ocpmetadata "github.com/cloud-bulldozer/go-commons/ocp-metadata"
	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	"github.com/spf13/cobra"
)

const clusterMetadataMetric = "clusterMetadata"

func setMetrics(cmd *cobra.Command, metricsProfile string) {
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
	os.Setenv("METRICS", strings.Join(metricsProfiles, ","))
}

// SetKubeBurnerFlags configures the required environment variables and flags for kube-burner
func GatherMetadata(wh *workloads.WorkloadHelper, alerting bool) error {
	var err error
	kubeClientProvider := config.NewKubeClientProvider("", "")
	_, restConfig := kubeClientProvider.DefaultClientSet()
	wh.MetadataAgent, err = ocpmetadata.NewMetadata(restConfig)
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
