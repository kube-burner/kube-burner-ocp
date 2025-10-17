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

package main

import (
	"embed"
	"os"
	"path/filepath"
	"time"

	uid "github.com/google/uuid"
	ocp "github.com/kube-burner/kube-burner-ocp"
	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/util"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

//go:embed config/*
var ocpConfig embed.FS

const (
	rootDir            = "config"
	metricsProfilesDir = rootDir + "/metrics-profiles"
	alertsDir          = rootDir + "/alerts-profiles"
	scriptsDir         = rootDir + "/scripts"
)

func openShiftCmd() *cobra.Command {
	var workloadConfig workloads.Config
	var wh workloads.WorkloadHelper
	var metricsProfileType string
	var esServer, esIndex string
	var QPS, burst int
	var gc, gcMetrics, alerting, checkHealth, localIndexing, extract, enableFileLogging bool
	ocpCmd := &cobra.Command{
		Use:  "kube-burner-ocp",
		Long: `kube-burner plugin designed to be used with OpenShift clusters as a quick way to run well-known workloads`,
	}
	ocpCmd.PersistentFlags().StringVar(&esServer, "es-server", "", "Elastic Search endpoint")
	ocpCmd.PersistentFlags().StringVar(&esIndex, "es-index", "", "Elastic Search index")
	ocpCmd.PersistentFlags().BoolVar(&localIndexing, "local-indexing", false, "Enable local indexing")
	ocpCmd.PersistentFlags().StringVar(&workloadConfig.MetricsEndpoint, "metrics-endpoint", "", "YAML file with a list of metric endpoints, overrides the es-server and es-index flags")
	ocpCmd.PersistentFlags().BoolVar(&alerting, "alerting", true, "Enable alerting")
	ocpCmd.PersistentFlags().BoolVar(&checkHealth, "check-health", true, "Check cluster health before job")
	ocpCmd.PersistentFlags().StringVar(&workloadConfig.UUID, "uuid", uid.NewString(), "Benchmark UUID")
	ocpCmd.PersistentFlags().DurationVar(&workloadConfig.Timeout, "timeout", 4*time.Hour, "Benchmark timeout")
	ocpCmd.PersistentFlags().IntVar(&QPS, "qps", 20, "QPS")
	ocpCmd.PersistentFlags().IntVar(&burst, "burst", 20, "Burst")
	ocpCmd.PersistentFlags().BoolVar(&gc, "gc", true, "Garbage collect created resources")
	ocpCmd.PersistentFlags().BoolVar(&gcMetrics, "gc-metrics", false, "Collect metrics during garbage collection")
	ocpCmd.PersistentFlags().StringVar(&workloadConfig.UserMetadata, "user-metadata", "", "User provided metadata file, in YAML format")
	ocpCmd.PersistentFlags().BoolVar(&extract, "extract", false, "Extract workload in the current directory")
	ocpCmd.PersistentFlags().StringVar(&metricsProfileType, "profile-type", "both", "Metrics profile to use, supported options are: regular, reporting or both")
	ocpCmd.PersistentFlags().BoolVar(&enableFileLogging, "enable-file-logging", true, "Enable file logging")
	ocpCmd.MarkFlagsRequiredTogether("es-server", "es-index")
	ocpCmd.MarkFlagsMutuallyExclusive("es-server", "metrics-endpoint")
	ocpCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if cmd.Name() == "version" || cmd.Name() == "help" || (cmd.HasParent() && cmd.Parent().Name() == "completion") {
			return
		}
		util.ConfigureLogging(cmd)
		if extract {
			if err := workloads.ExtractWorkload(ocpConfig, rootDir, []string{cmd.Name(), "alerts-profiles", "metrics-profiles"}); err != nil {
				log.Fatal(err.Error())
			}
			os.Exit(0)
		}
		if enableFileLogging {
			util.SetupFileLogging("ocp-" + workloadConfig.UUID)
		}
		if checkHealth && (cmd.Name() != "cluster-health" || cmd.Name() == "index") {
			ocp.ClusterHealthCheck()
		}
		kubeClientProvider := config.NewKubeClientProvider("", "")
		workloadDir := filepath.Join(rootDir, cmd.Name())
		wh = workloads.NewWorkloadHelper(workloadConfig, &ocpConfig, workloadDir, metricsProfilesDir, alertsDir, scriptsDir, kubeClientProvider)

		// Set common variables that all workloads can use
		ocp.AdditionalVars = map[string]any{
			"UUID":           workloadConfig.UUID,
			"QPS":            QPS,
			"BURST":          burst,
			"GC":             gc,
			"GC_METRICS":     gcMetrics,
			"LOCAL_INDEXING": localIndexing,
		}
		if alerting {
			ocp.AdditionalVars["ALERTS"] = "alerts.yml"
		} else {
			ocp.AdditionalVars["ALERTS"] = ""
		}
		if workloadConfig.MetricsEndpoint == "" {
			ocp.AdditionalVars["ES_SERVER"] = esServer
			ocp.AdditionalVars["ES_INDEX"] = esIndex
		}

		if err := ocp.GatherMetadata(&wh, alerting); err != nil {
			log.Fatal(err.Error())
		}
	}
	ocpCmd.AddCommand(
		ocp.NewClusterDensity(&wh, "cluster-density-v2"),
		ocp.NewClusterDensity(&wh, "cluster-density-ms"),
		ocp.NewCrdScale(&wh),
		ocp.NewUdnBgp(&wh, "udn-bgp"),
		ocp.NewNetworkPolicy(&wh, "network-policy"),
		ocp.NewOLMv1(&wh, "olm"),
		ocp.NewNodeDensity(&wh, "node-density"),
		ocp.NewNodeDensity(&wh, "node-density-heavy"),
		ocp.NewNodeDensity(&wh, "node-density-cni"),
		ocp.NewNodeScale(&wh, "node-scale"),
		ocp.NewUDNDensityPods(&wh),
		ocp.NewIndex(&wh, ocpConfig),
		ocp.NewPVCDensity(&wh),
		ocp.NewRDSCore(&wh),
		ocp.NewWebBurner(&wh, "web-burner-init"),
		ocp.NewWebBurner(&wh, "web-burner-node-density"),
		ocp.NewWebBurner(&wh, "web-burner-cluster-density"),
		ocp.NewEgressIP(&wh, "egressip"),
		ocp.NewWhereabouts(&wh),
		ocp.NewVirtDensity(&wh),
		ocp.NewVirtUDNDensity(&wh),
		ocp.ClusterHealth(),
		ocp.CustomWorkload(&wh),
		ocp.NewVirtCapacityBenchmark(&wh),
		ocp.NewVirtClone(&wh),
		ocp.NewVirtEphemeralRestart(&wh),
		ocp.NewDVClone(&wh),
		ocp.NewVirtMigration(&wh),
		ocp.NewKueueOperator(&wh, "kueue-operator-pods"),
		ocp.NewKueueOperator(&wh, "kueue-operator-jobs"),
		ocp.NewKueueOperator(&wh, "kueue-operator-jobs-shared"),
		ocp.NewANPDensityPods(&wh, "anp-density-pods"),
	)
	util.SetupCmd(ocpCmd)
	return ocpCmd
}

func main() {
	if openShiftCmd().Execute() != nil {
		os.Exit(1)
	}
}
