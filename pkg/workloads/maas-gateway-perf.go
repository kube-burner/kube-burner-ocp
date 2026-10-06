// Copyright 2026 The Kube-burner Authors.
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
	"net/url"
	"os"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	deadlineFactor = 3
	deadlineGrace  = 5 * time.Minute
)

// autoJobDeadline covers every sample batch, since activeDeadlineSeconds applies
// to the whole Job rather than a single pod.
func autoJobDeadline(samples, parallelism int, warmup, benchmarkDuration time.Duration) time.Duration {
	if samples < 1 {
		samples = 1
	}
	if parallelism < 1 {
		parallelism = 1
	}
	batches := (samples + parallelism - 1) / parallelism
	return time.Duration(batches)*(warmup+benchmarkDuration)*deadlineFactor + deadlineGrace
}

// NewMaasGatewayPerf holds MaaS Gateway performance benchmark workload
func NewMaasGatewayPerf(wh *workloads.WorkloadHelper) *cobra.Command {
	var gatewayHost, gatewayIP, simulatorHost, providers, payloadSizes, concurrencyLevels string
	var guidellmImage string
	var benchmarkDuration, warmup, pause, jobDeadline time.Duration
	var samples, parallelism int
	var metricsProfiles []string
	var rc int

	cmd := &cobra.Command{
		Use:   "maas-gateway-perf",
		Short: "Runs maas-gateway-perf workload",
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["GATEWAY_HOST"] = gatewayHost
			AdditionalVars["GATEWAY_IP"] = gatewayIP
			u, err := url.Parse(gatewayHost)
			if err != nil || u.Scheme == "" || u.Hostname() == "" {
				log.Fatalf("Invalid --gateway-host %q: expected a full URL including scheme and host, e.g. https://maas.apps.example.com", gatewayHost)
			}
			AdditionalVars["GATEWAY_HOSTNAME"] = u.Hostname()
			AdditionalVars["SIMULATOR_HOST"] = simulatorHost
			AdditionalVars["PROVIDERS"] = providers
			AdditionalVars["PAYLOAD_SIZES"] = payloadSizes
			AdditionalVars["CONCURRENCY_LEVELS"] = concurrencyLevels
			if jobDeadline <= 0 {
				jobDeadline = autoJobDeadline(samples, parallelism, warmup, benchmarkDuration)
			}
			log.Infof("Per-Job deadline (activeDeadlineSeconds): %s", jobDeadline)
			AdditionalVars["JOB_DEADLINE"] = int(jobDeadline.Seconds())
			AdditionalVars["BENCHMARK_DURATION"] = int(benchmarkDuration.Seconds())
			AdditionalVars["WARMUP"] = int(warmup.Seconds())
			AdditionalVars["GUIDELLM_IMAGE"] = guidellmImage
			AdditionalVars["SAMPLES"] = samples
			AdditionalVars["PARALLELISM"] = parallelism
			AdditionalVars["PAUSE"] = pause
			rc = RunWorkload(cmd, wh, "maas-gateway-perf.yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVar(&gatewayHost, "gateway-host", "", "Full gateway URL (e.g., https://maas.apps.example.com)")
	cmd.Flags().StringVar(&gatewayIP, "gateway-ip", "", "Gateway ClusterIP for in-cluster DNS override")
	cmd.Flags().StringVar(&simulatorHost, "simulator-host", "", "Simulator URL (e.g., http://llm-d-inference-sim.llm.svc.cluster.local:8000)")
	cmd.Flags().StringVar(&providers, "providers", "gpt-4o-openai,claude-sonnet-anthropic", "Comma-separated provider model names")
	cmd.Flags().StringVar(&payloadSizes, "payload-sizes", "small,medium", "Comma-separated payload sizes from: small(32/64), medium(256/512), large(1024/1024), very-large(2048/2048)")
	cmd.Flags().StringVar(&concurrencyLevels, "concurrency-levels", "8,32,64,128,512", "Comma-separated concurrency levels")
	cmd.Flags().DurationVar(&jobDeadline, "job-deadline", 0, "Hard per-Job deadline (activeDeadlineSeconds); 0 derives it from samples, warmup and benchmark-duration")
	cmd.Flags().DurationVar(&benchmarkDuration, "benchmark-duration", 90*time.Second, "Duration of each benchmark run")
	cmd.Flags().DurationVar(&warmup, "warmup", 30*time.Second, "Warmup period discarded from results")
	cmd.Flags().StringVar(&guidellmImage, "guidellm-image", "ghcr.io/cloud-bulldozer/guidellm-results-parser:v0.0.4", "GuideLLM container image with parser")
	cmd.Flags().IntVar(&samples, "samples", 3, "Number of benchmark samples (Job completions)")
	cmd.Flags().IntVar(&parallelism, "parallelism", 1, "Job parallelism for benchmark runs")
	cmd.Flags().DurationVar(&pause, "pause", 10*time.Second, "Pause after each benchmark job completes")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"maas-gateway-perf-metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("gateway-host")
	cmd.MarkFlagRequired("simulator-host")
	return cmd
}
