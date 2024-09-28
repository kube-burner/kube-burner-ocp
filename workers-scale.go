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

package ocp

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cloud-bulldozer/go-commons/indexers"
	ocpmetadata "github.com/cloud-bulldozer/go-commons/ocp-metadata"
	"github.com/cloud-bulldozer/go-commons/version"
	"github.com/kube-burner/kube-burner/pkg/burner"
	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/prometheus"
	"github.com/kube-burner/kube-burner/pkg/util/metrics"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	wscale "kube-burner.io/ocp/pkg/workerscale"
)

// NewWorkersScale orchestrates scaling workers in ocp wrapper
func NewWorkersScale(metricsEndpoint *string, ocpMetaAgent *ocpmetadata.Metadata) *cobra.Command {
	var enableAutoscaler, isHCP bool
	var metricsProfile, mcKubeConfig string
	var userMetadata, metricsDirectory string
	var prometheusStep time.Duration
	var uuid string
	var err error
	var scaleEventEpoch int64
	var rc, additionalWorkerNodes int
	var prometheusURL, prometheusToken string
	var tarballName string
	var indexer config.MetricsEndpoint
	var clusterMetadataMap map[string]interface{}
	const autoScaled = "autoScaled"
	cmd := &cobra.Command{
		Use:          "workers-scale",
		Short:        "Runs workers-scale sub-command",
		Long:         "If no other indexer is specified, local indexer is used by default",
		SilenceUsage: true,
		PostRun: func(cmd *cobra.Command, args []string) {
			log.Info("👋 Exiting kube-burner ", uuid)
			os.Exit(rc)
		},
		Run: func(cmd *cobra.Command, args []string) {
			start := time.Now().Unix()
			uuid, _ = cmd.Flags().GetString("uuid")
			esServer, _ := cmd.Flags().GetString("es-server")
			esIndex, _ := cmd.Flags().GetString("es-index")
			gc, _ := cmd.Flags().GetBool("gc")
			workloads.ConfigSpec.GlobalConfig.UUID = uuid
			// When metricsEndpoint is specified, don't fetch any prometheus token
			if *metricsEndpoint == "" {
				prometheusURL, prometheusToken, err = ocpMetaAgent.GetPrometheus()
				if err != nil {
					log.Fatal("Error obtaining prometheus information from cluster: ", err.Error())
				}
			}
			metricsProfiles := strings.FieldsFunc(metricsProfile, func(r rune) bool {
				return r == ',' || r == ' '
			})
			indexer = config.MetricsEndpoint{
				Endpoint:      prometheusURL,
				Token:         prometheusToken,
				Step:          prometheusStep,
				Metrics:       metricsProfiles,
				SkipTLSVerify: true,
			}
			if esServer != "" && esIndex != "" {
				indexer.IndexerConfig = indexers.IndexerConfig{
					Type:    indexers.ElasticIndexer,
					Servers: []string{esServer},
					Index:   esIndex,
				}
			} else {
				if metricsDirectory == "collected-metrics" {
					metricsDirectory = metricsDirectory + "-" + uuid
				}
				indexer.IndexerConfig = indexers.IndexerConfig{
					Type:             indexers.LocalIndexer,
					MetricsDirectory: metricsDirectory,
					TarballName:      tarballName,
				}
			}

			clusterMetadata, err = ocpMetaAgent.GetClusterMetadata()
			if err != nil {
				log.Fatal("Error obtaining clusterMetadata: ", err.Error())
			}
			metadata := make(map[string]interface{})
			jsonData, _ := json.Marshal(clusterMetadata)
			json.Unmarshal(jsonData, &clusterMetadataMap)
			for k, v := range clusterMetadataMap {
				metadata[k] = v
			}
			if enableAutoscaler {
				metadata[autoScaled] = true
			} else {
				metadata[autoScaled] = false
			}
			workloads.ConfigSpec.MetricsEndpoints = append(workloads.ConfigSpec.MetricsEndpoints, indexer)
			metricsScraper := metrics.ProcessMetricsScraperConfig(metrics.ScraperConfig{
				ConfigSpec:      &workloads.ConfigSpec,
				MetricsEndpoint: *metricsEndpoint,
				UserMetaData:    userMetadata,
				RawMetadata:     metadata,
			})
			var indexerValue indexers.Indexer
			for _, value := range metricsScraper.IndexerList {
				indexerValue = value
				break
			}
			scenario := fetchScenario(enableAutoscaler, clusterMetadata)
			if _, ok := scenario.(*wscale.RosaScenario); ok {
				if clusterMetadata.MasterNodesCount == 0 && clusterMetadata.InfraNodesCount == 0 {
					isHCP = true
				}
			} else {
				isHCP = false
			}
			scenario.OrchestrateWorkload(wscale.ScaleConfig{
				UUID:                  uuid,
				AdditionalWorkerNodes: additionalWorkerNodes,
				Metadata:              metricsScraper.Metadata,
				Indexer:               indexerValue,
				GC:                    gc,
				ScaleEventEpoch:       scaleEventEpoch,
				AutoScalerEnabled:     enableAutoscaler,
				MCKubeConfig:          mcKubeConfig,
				IsHCP:                 isHCP,
			})
			end := time.Now().Unix()
			for _, prometheusClient := range metricsScraper.PrometheusClients {
				prometheusJob := prometheus.Job{
					Start: time.Unix(start, 0),
					End:   time.Unix(end, 0),
					JobConfig: config.Job{
						Name: wscale.JobName,
					},
				}
				if prometheusClient.ScrapeJobsMetrics(prometheusJob) != nil {
					rc = 1
				}
			}
			if workloads.ConfigSpec.MetricsEndpoints[0].Type == indexers.LocalIndexer && tarballName != "" {
				if err := metrics.CreateTarball(workloads.ConfigSpec.MetricsEndpoints[0].IndexerConfig); err != nil {
					log.Fatal(err)
				}
			}
			jobSummary := burner.JobSummary{
				Timestamp:    time.Unix(start, 0).UTC(),
				EndTimestamp: time.Unix(end, 0).UTC(),
				ElapsedTime:  time.Unix(end, 0).UTC().Sub(time.Unix(start, 0).UTC()).Round(time.Second).Seconds(),
				UUID:         uuid,
				JobConfig: config.Job{
					Name: wscale.JobName,
				},
				Metadata:   metricsScraper.Metadata,
				MetricName: "jobSummary",
				Version:    fmt.Sprintf("%v@%v", version.Version, version.GitCommit),
				Passed:     rc == 0,
			}
			burner.IndexJobSummary([]burner.JobSummary{jobSummary}, indexerValue)
		},
	}
	cmd.Flags().StringVarP(&metricsProfile, "metrics-profile", "m", "metrics.yml", "Comma-separated list of metric profiles")
	cmd.Flags().StringVar(&metricsDirectory, "metrics-directory", "collected-metrics", "Directory to dump the metrics files in, when using default local indexing")
	cmd.Flags().StringVar(&mcKubeConfig, "mc-kubeconfig", "", "Path for management cluster kubeconfig")
	cmd.Flags().DurationVar(&prometheusStep, "step", 30*time.Second, "Prometheus step size")
	cmd.Flags().IntVar(&additionalWorkerNodes, "additional-worker-nodes", 3, "Additional workers to scale")
	cmd.Flags().BoolVar(&enableAutoscaler, "enable-autoscaler", false, "Enables autoscaler while scaling the cluster")
	cmd.Flags().Int64Var(&scaleEventEpoch, "scale-event-epoch", 0, "Scale event epoch time")
	cmd.Flags().StringVar(&userMetadata, "user-metadata", "", "User provided metadata file, in YAML format")
	cmd.Flags().StringVar(&tarballName, "tarball-name", "", "Dump collected metrics into a tarball with the given name, requires local indexing")
	cmd.Flags().SortFlags = false
	return cmd
}

// FetchScenario helps us to fetch relevant class
func fetchScenario(enableAutoscaler bool, clusterMetadata ocpmetadata.ClusterMetadata) wscale.Scenario {
	if clusterMetadata.ClusterType == "rosa" {
		return &wscale.RosaScenario{}
	} else {
		if enableAutoscaler {
			return &wscale.AutoScalerScenario{}
		}
		return &wscale.BaseScenario{}
	}
}
