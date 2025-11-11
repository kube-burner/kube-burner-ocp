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

package workloads

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"maps"

	"github.com/cloud-bulldozer/go-commons/v2/indexers"
	"github.com/cloud-bulldozer/go-commons/v2/version"
	"github.com/kube-burner/kube-burner/pkg/burner"
	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/prometheus"
	"github.com/kube-burner/kube-burner/pkg/util/metrics"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// NewIndex orchestrates indexing for ocp wrapper
func NewIndex(wh *workloads.WorkloadHelper, ocpConfig embed.FS) *cobra.Command {
	var jobName string
	var metricsProfiles []string
	var start, end int64
	var userMetadata, metricsDirectory string
	var prometheusStep time.Duration
	var uuid string
	var rc int
	var prometheusURL, prometheusToken string
	var tarballName string
	var indexer config.MetricsEndpoint
	var clusterMetadataMap map[string]any
	cmd := &cobra.Command{
		Use:          "index",
		Short:        "Runs index sub-command",
		Long:         "If no other indexer is specified, local indexer is used by default",
		SilenceUsage: true,
		PostRun: func(cmd *cobra.Command, args []string) {
			log.Info("👋 Exiting kube-burner ", uuid)
			os.Exit(rc)
		},
		Run: func(cmd *cobra.Command, args []string) {
			jobEnd := end
			uuid, _ = cmd.Flags().GetString("uuid")
			clusterMetadata, err := wh.MetadataAgent.GetClusterMetadata()
			if err != nil {
				log.Fatal("Error obtaining clusterMetadata: ", err.Error())
			}
			esServer, _ := cmd.Flags().GetString("es-server")
			esIndex, _ := cmd.Flags().GetString("es-index")
			workloads.ConfigSpec.GlobalConfig.UUID = uuid
			// When metricsEndpoint is specified, don't fetch any prometheus token
			if wh.MetricsEndpoint == "" {
				prometheusURL, prometheusToken, err = wh.MetadataAgent.GetPrometheus()
				if err != nil {
					log.Fatal("Error obtaining prometheus information from cluster: ", err.Error())
				}
			}
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

			metadata := make(map[string]any)
			jsonData, _ := json.Marshal(clusterMetadata)
			json.Unmarshal(jsonData, &clusterMetadataMap)
			maps.Copy(metadata, clusterMetadataMap)
			workloads.ConfigSpec.MetricsEndpoints = append(workloads.ConfigSpec.MetricsEndpoints, indexer)
			metricsScraper := metrics.ProcessMetricsScraperConfig(metrics.ScraperConfig{
				ConfigSpec:      &workloads.ConfigSpec,
				MetricsEndpoint: wh.MetricsEndpoint,
				UserMetaData:    userMetadata,
				MetricsMetadata: metadata,
			})
			for _, prometheusClient := range metricsScraper.PrometheusClients {
				prometheusJob := prometheus.Job{
					Start: time.Unix(start, 0),
					End:   time.Unix(end+TenMinutes, 0),
					JobConfig: config.Job{
						Name: jobName,
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
			var indexerValue indexers.Indexer
			for _, value := range metricsScraper.IndexerList {
				indexerValue = value
				break
			}
			jobSummary := burner.JobSummary{
				Timestamp:    time.Unix(start, 0).UTC(),
				EndTimestamp: time.Unix(jobEnd, 0).UTC(),
				ElapsedTime:  time.Unix(jobEnd, 0).UTC().Sub(time.Unix(start, 0).UTC()).Round(time.Second).Seconds(),
				UUID:         uuid,
				JobConfig: config.Job{
					Name: jobName,
				},
				Metadata:   metricsScraper.SummaryMetadata,
				MetricName: "jobSummary",
				Version:    fmt.Sprintf("%v@%v", version.Version, version.GitCommit),
				Passed:     rc == 0,
			}
			burner.IndexJobSummary([]burner.JobSummary{jobSummary}, indexerValue)
		},
	}
	cmd.Flags().StringSliceVarP(&metricsProfiles, "metrics-profile", "m", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().StringVar(&metricsDirectory, "metrics-directory", "collected-metrics", "Directory to dump the metrics files in, when using default local indexing")
	cmd.Flags().DurationVar(&prometheusStep, "step", 30*time.Second, "Prometheus step size")
	cmd.Flags().Int64Var(&start, "start", time.Now().Unix()-3600, "Epoch start time")
	cmd.Flags().Int64Var(&end, "end", time.Now().Unix(), "Epoch end time")
	cmd.Flags().StringVar(&jobName, "job-name", "kube-burner-ocp-indexing", "Indexing job name")
	cmd.Flags().StringVar(&userMetadata, "user-metadata", "", "User provided metadata file, in YAML format")
	cmd.Flags().StringVar(&tarballName, "tarball-name", "", "Dump collected metrics into a tarball with the given name, requires local indexing")
	cmd.Flags().SortFlags = false
	return cmd
}
