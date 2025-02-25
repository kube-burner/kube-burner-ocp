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
	"encoding/json"
	"fmt"
	"os"
	"strings"

	k8sconnector "github.com/cloud-bulldozer/go-commons/v2/k8s-connector"
	ocpmetadata "github.com/cloud-bulldozer/go-commons/v2/ocp-metadata"
	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var clusterMetadata ocpmetadata.ClusterMetadata

func setMetrics(cmd *cobra.Command, metricsProfiles []string) {
	profileType, _ := cmd.Root().PersistentFlags().GetString("profile-type")
	switch ProfileType(profileType) {
	case Reporting:
		metricsProfiles = []string{"metrics-report.yml"}
	case Both:
		metricsProfiles = append(metricsProfiles, "metrics-report.yml")
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
	if alerting && wh.Config.MetricsEndpoint == "" {
		wh.Config.PrometheusURL, wh.Config.PrometheusToken, err = wh.MetadataAgent.GetPrometheus()
		if err != nil {
			return fmt.Errorf("error obtaining Prometheus information: %v", err)
		}
	}
	clusterMetadata, err = wh.MetadataAgent.GetClusterMetadata()
	if err != nil {
		return err
	}
	jsonData, err := json.Marshal(clusterMetadata)
	if err != nil {
		return err
	}
	json.Unmarshal(jsonData, &wh.SummaryMetadata)
	wh.MetricsMetadata = map[string]interface{}{
		"ocpMajorVersion": clusterMetadata.OCPMajorVersion,
		"ocpVersion":      clusterMetadata.OCPVersion,
	}
	return nil
}

func getK8SConnector() k8sconnector.K8SConnector {
	kubeClientProvider := config.NewKubeClientProvider("", "")
	_, restConfig := kubeClientProvider.DefaultClientSet()
	k8sConnector, err := k8sconnector.NewK8SConnector(restConfig)
	if err != nil {
		log.Fatal(err)
	}
	return k8sConnector
}

func generateLoopCounterSlice(length int) []string {
	counter := make([]string, length)
	for i := 0; i < length; i++ {
		counter[i] = fmt.Sprint(i + 1)
	}
	return counter
}

func roundUpToMultiple(num, multiple int) int {
	if multiple == 0 {
		return num // Avoid division by zero
	}
	return ((num + multiple - 1) / multiple) * multiple
}
