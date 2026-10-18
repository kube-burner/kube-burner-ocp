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
	"strings"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	deadlineFactor        = 3
	deadlineGrace         = 5 * time.Minute
	defaultIdentitySecret = "maas-perf-identities"
)

// supportedLoadProfiles are the GuideLLM profile kinds this workload knows how
// to parameterize. constant and poisson are open loop (the level is a request
// rate); concurrent is closed loop (the level is a number of streams).
var supportedLoadProfiles = map[string]bool{
	"constant":   true,
	"poisson":    true,
	"concurrent": true,
}

// normalizeTargets validates which halves of the A/B matrix to run. Running
// baseline alone is useful when the gateway's auth is not yet usable.
func normalizeTargets(targets string) string {
	valid := map[string]bool{"baseline": true, "gateway": true}
	var out []string
	for _, t := range strings.Split(targets, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !valid[t] {
			log.Fatalf("Invalid --targets entry %q: supported values are baseline, gateway", t)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		log.Fatal("--targets must contain at least one of baseline, gateway")
	}
	return strings.Join(out, ",")
}

// normalizeLoadProfiles trims and validates the comma-separated profile list.
func normalizeLoadProfiles(loadProfiles string) string {
	var profiles []string
	for _, profile := range strings.Split(loadProfiles, ",") {
		profile = strings.TrimSpace(profile)
		if profile == "" {
			continue
		}
		if !supportedLoadProfiles[profile] {
			log.Fatalf("Invalid --load-profiles entry %q: supported kinds are constant, poisson, concurrent", profile)
		}
		profiles = append(profiles, profile)
	}
	if len(profiles) == 0 {
		log.Fatal("--load-profiles must contain at least one profile kind")
	}
	return strings.Join(profiles, ",")
}

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
	var guidellmImage, loadProfiles, apiKeySecret string
	var meteringNamespace, tokenizer, tokenizerConfigMap, targets string
	var benchmarkDuration, warmup, pause, jobDeadline time.Duration
	var samples, parallelism, identities int
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
			AdditionalVars["LOAD_PROFILES"] = normalizeLoadProfiles(loadProfiles)

			// --identities mints its own pool, so it implies the Secret name and
			// sizes the Job to run one pod per tenant concurrently: every sample
			// is then a full sweep of the pool rather than a single tenant.
			if identities > 0 {
				if meteringNamespace == "" {
					log.Fatal("--identities requires --metering-namespace: the namespace of the metering-service that mints the tenants")
				}
				if apiKeySecret == "" {
					apiKeySecret = defaultIdentitySecret
				}
				if parallelism < identities {
					log.Infof("Raising --parallelism to %d so all %d identities are active concurrently", identities, identities)
					parallelism = identities
				}
			}
			completions := samples
			if identities > 0 {
				completions = samples * identities
			}
			if apiKeySecret != "" && parallelism < 2 {
				log.Warnf("--api-key-secret is set but --parallelism is %d: only one identity will be active at a time. Raise --parallelism to simulate concurrent tenants.", parallelism)
			}
			AdditionalVars["API_KEY_SECRET"] = apiKeySecret
			AdditionalVars["IDENTITIES"] = identities
			AdditionalVars["METERING_NAMESPACE"] = meteringNamespace
			AdditionalVars["COMPLETIONS"] = completions
			AdditionalVars["TOKENIZER"] = tokenizer
			AdditionalVars["TOKENIZER_CONFIGMAP"] = tokenizerConfigMap
			AdditionalVars["TARGETS"] = normalizeTargets(targets)
			if jobDeadline <= 0 {
				jobDeadline = autoJobDeadline(completions, parallelism, warmup, benchmarkDuration)
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
	cmd.Flags().StringVar(&concurrencyLevels, "concurrency-levels", "8,32,64,128,512", "Comma-separated load levels; requests/sec for the constant and poisson profiles (open loop), concurrent streams for the concurrent profile (closed loop)")
	cmd.Flags().StringVar(&targets, "targets", "baseline,gateway", "Which halves of the A/B matrix to run: baseline (direct to simulator), gateway, or both")
	cmd.Flags().StringVar(&loadProfiles, "load-profiles", "constant", "Comma-separated GuideLLM profile kinds: constant (open loop, uniform arrivals), poisson (open loop, random arrivals), concurrent (closed loop, fixed streams)")
	cmd.Flags().StringVar(&apiKeySecret, "api-key-secret", "", "Pre-existing Secret with a keys.txt of 'user<TAB>key' rows; each indexed pod binds to one row. Defaults to the ServiceAccount token when unset")
	cmd.Flags().IntVar(&identities, "identities", 0, "Mint this many MaaS tenants before the run and revoke them after; implies --api-key-secret "+defaultIdentitySecret+" and raises --parallelism to match. 0 disables minting")
	cmd.Flags().StringVar(&meteringNamespace, "metering-namespace", "", "Namespace of the metering-service used to mint and revoke identities; required with --identities")
	cmd.Flags().StringVar(&tokenizer, "tokenizer", "gpt2", "Tokenizer for synthetic prompt generation: a HuggingFace model id (needs egress to huggingface.co) or a local path when --tokenizer-configmap is set")
	cmd.Flags().StringVar(&tokenizerConfigMap, "tokenizer-configmap", "", "ConfigMap with pre-downloaded tokenizer files; mounted at /tokenizer and runs GuideLLM fully offline. Required on clusters without egress to huggingface.co")
	cmd.Flags().DurationVar(&jobDeadline, "job-deadline", 0, "Hard per-Job deadline (activeDeadlineSeconds); 0 derives it from samples, warmup and benchmark-duration")
	cmd.Flags().DurationVar(&benchmarkDuration, "benchmark-duration", 90*time.Second, "Duration of each benchmark run")
	cmd.Flags().DurationVar(&warmup, "warmup", 30*time.Second, "Warmup period discarded from results")
	cmd.Flags().StringVar(&guidellmImage, "guidellm-image", "ghcr.io/cloud-bulldozer/guidellm-results-parser:v0.0.5", "GuideLLM container image with parser")
	cmd.Flags().IntVar(&samples, "samples", 3, "Number of benchmark samples (Job completions)")
	cmd.Flags().IntVar(&parallelism, "parallelism", 1, "Job parallelism for benchmark runs")
	cmd.Flags().DurationVar(&pause, "pause", 10*time.Second, "Pause after each benchmark job completes")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"maas-gateway-perf-metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("gateway-host")
	cmd.MarkFlagRequired("simulator-host")
	return cmd
}
