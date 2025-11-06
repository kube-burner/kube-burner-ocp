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
	"os"
	"time"

	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"

	"github.com/spf13/cobra"
)

// NewNodeDensity holds node-density-cni workload
func NewRDSCore(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations, churnPercent, churnCycles, dpdkCores int
	var churn, svcLatency bool
	var churnDelay, churnDuration, podReadyThreshold time.Duration
	var deletionStrategy, perfProfile, dpdkHugepages, dpdkDevicepool, netDevicepool string
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "rds-core",
		Short:        "Runs rds-core workload",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			ingressDomain, err := wh.MetadataAgent.GetDefaultIngressDomain()
			if err != nil {
				log.Fatal("Error obtaining default ingress domain: ", err.Error())
			}
			AdditionalVars["CHURN"] = churn
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["DPDK_CORES"] = dpdkCores
			AdditionalVars["DPDK_HUGEPAGES"] = dpdkHugepages
			AdditionalVars["SRIOV_DPDK_DEVICEPOOL"] = sriovDpdkDevicepool
			AdditionalVars["SRIOV_NET_DEVICEPOOL"] = sriovNetDevicepool
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["PERF_PROFILE"] = perfProfile
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["SVC_LATENCY"] = svcLatency
			AdditionalVars["INGRESS_DOMAIN"] = ingressDomain
			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().BoolVar(&churn, "churn", true, "Enable churning")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 1*time.Hour, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&deletionStrategy, "churn-deletion-strategy", "default", "Churn deletion strategy to use")
	cmd.Flags().IntVar(&dpdkCores, "dpdk-cores", 2, "Number of cores per DPDK pod")
	cmd.Flags().StringVar(&dpdkHugepages, "dpdk-hugepages", "16Gi", "Number of hugepages per DPDK pod. Must be a multiple of 1Gi")
	cmd.Flags().StringVar(&sriovDpdkDevicepool, "dpdk-devicepool", "intelnic2", "SRIOV Device pool name for DPDK VFs in the cluster")
	cmd.Flags().StringVar(&sriovNetDevicepool, "net-devicepool", "intelnic2", "SRIOV Device pool name for Kernel VFs in the cluster")
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Number of iterations/namespaces")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().StringVar(&perfProfile, "perf-profile", "default", "Performance profile implemented in the cluster")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 2*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	return cmd
}
