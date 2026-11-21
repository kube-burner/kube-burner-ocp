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
	"os"
	"time"

	kubeburnermeasurements "github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"github.com/kube-burner/kube-burner-ocp/pkg/measurements"
	"github.com/spf13/cobra"
)

var ztwimSvidMeasurementFactoryMap = map[string]kubeburnermeasurements.NewMeasurementFactory{
	"ztwimSvidVerification": measurements.NewZtwimSvidVerificationMeasurementFactory,
	"ztwimSvidLatency":      measurements.NewZtwimSvidLatencyMeasurementFactory,
}

// NewZtwimSvidIssuance holds the ztwim-svid-issuance workload
func NewZtwimSvidIssuance(wh *workloads.WorkloadHelper) *cobra.Command {
	var rc int
	var metricsProfiles []string
	var podReplicas, iterations, QPS, burst int
	var workloadRuntime, spiffeClass, spiffeHelperImage, appImage string
	var svidTTL, rotationSoak, svidLatencyMaxWait, svidLatencyPollInterval time.Duration

	cmd := &cobra.Command{
		Use:          "ztwim-svid-issuance",
		Short:        "Runs ztwim-svid-issuance workload",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			AdditionalVars["POD_REPLICAS"] = podReplicas
			AdditionalVars["WORKLOAD_RUNTIME"] = workloadRuntime
			AdditionalVars["ITERATIONS"] = iterations
			AdditionalVars["SPIFFE_CLASS"] = spiffeClass
			AdditionalVars["SPIFFE_HELPER_IMAGE"] = spiffeHelperImage
			AdditionalVars["APP_IMAGE"] = appImage
			AdditionalVars["APP_LABEL"] = "ztwim-perf"
			AdditionalVars["QPS"] = QPS
			AdditionalVars["BURST"] = burst
			AdditionalVars["SVID_TTL"] = svidTTL.String()
			AdditionalVars["ROTATION_SOAK"] = rotationSoak.String()

			measurements.ZtwimSvidMeasurementConfig.Namespace = "ztwim-perf"
			measurements.ZtwimSvidMeasurementConfig.AppContainer = "app"
			measurements.ZtwimSvidMeasurementConfig.AppLabel = "ztwim-perf"
			measurements.ZtwimSvidMeasurementConfig.SVIDTTL = svidTTL
			measurements.ZtwimSvidMeasurementConfig.RotationSoak = rotationSoak
			measurements.ZtwimSvidMeasurementConfig.SvidLatencyMaxWait = svidLatencyMaxWait
			measurements.ZtwimSvidMeasurementConfig.SvidLatencyPollInterval = svidLatencyPollInterval

			setMetrics(cmd, metricsProfiles)
			wh.SetMeasurements(ztwimSvidMeasurementFactoryMap)
			rc = RunWorkload(cmd, wh, "ztwim-svid-issuance.yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}

	cmd.Flags().IntVar(&podReplicas, "pod-replicas", 100, "Attestation pods per iteration")
	cmd.Flags().StringVar(&workloadRuntime, "workload-runtime", "3600s", "App container active duration before idle (pods stay running)")
	cmd.Flags().IntVar(&iterations, "iterations", 1, "Job iterations")
	cmd.Flags().StringVar(&spiffeClass, "spiffe-class", "zero-trust-workload-identity-manager-spire", "ClusterSPIFFEID className")
	cmd.Flags().StringVar(&spiffeHelperImage, "spiffe-helper-image", "ghcr.io/spiffe/spiffe-helper:0.11.0", "spiffe-helper image")
	cmd.Flags().StringVar(&appImage, "app-image", "quay.io/prometheus/busybox", "App container image")
	cmd.Flags().DurationVar(&svidTTL, "svid-ttl", 60*time.Second, "ClusterSPIFFEID X.509 SVID TTL (drives rotation during soak)")
	cmd.Flags().DurationVar(&rotationSoak, "rotation-soak", 150*time.Second, "Pause after pods are ready to allow SVID rotation (0 disables rotation check)")
	cmd.Flags().DurationVar(&svidLatencyMaxWait, "svid-latency-timeout", 5*time.Minute, "Max wait for svid.pem (ztwimSvidLatency and ztwimSvidVerification)")
	cmd.Flags().DurationVar(&svidLatencyPollInterval, "svid-latency-poll-interval", 2*time.Second, "Poll interval when waiting for svid.pem in ZTWIM SVID measurements")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"ztwim-metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.PersistentFlags().IntVar(&QPS, "qps", 10, "QPS")
	cmd.PersistentFlags().IntVar(&burst, "burst", 10, "Burst")
	return cmd
}
