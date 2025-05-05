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
	"fmt"
	"os"
	"strconv"
	"strings"
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
	var churnDeletionStrategy, perfProfile string
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "rds-core",
		Short:        "Runs rds-core workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			os.Setenv("CHURN", fmt.Sprint(churn))
			os.Setenv("CHURN_CYCLES", fmt.Sprintf("%v", churnCycles))
			os.Setenv("CHURN_DURATION", fmt.Sprintf("%v", churnDuration))
			os.Setenv("CHURN_DELAY", fmt.Sprintf("%v", churnDelay))
			os.Setenv("CHURN_PERCENT", fmt.Sprint(churnPercent))
			os.Setenv("CHURN_DELETION_STRATEGY", churnDeletionStrategy)
			os.Setenv("DPDK_CORES", fmt.Sprint(dpdkCores))
			os.Setenv("JOB_ITERATIONS", fmt.Sprint(iterations))
			os.Setenv("PERF_PROFILE", perfProfile)
			os.Setenv("POD_READY_THRESHOLD", fmt.Sprintf("%v", podReadyThreshold))
			os.Setenv("SVC_LATENCY", strconv.FormatBool(svcLatency))
			ingressDomain, err := wh.MetadataAgent.GetDefaultIngressDomain()
			if err != nil {
				log.Fatal("Error obtaining default ingress domain: ", err.Error())
			}
			os.Setenv("INGRESS_DOMAIN", ingressDomain)
		},
		Run: func(cmd *cobra.Command, args []string) {
			os.Setenv("METRICS", strings.Join(metricsProfiles, ","))
			rc = wh.Run(cmd.Name())
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
	cmd.Flags().StringVar(&churnDeletionStrategy, "churn-deletion-strategy", "default", "Churn deletion strategy to use")
	cmd.Flags().IntVar(&dpdkCores, "dpdk-cores", 2, "Number of cores per DPDK pod")
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Number of iterations/namespaces")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().StringVar(&perfProfile, "perf-profile", "default", "Performance profile implemented in the cluster")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 2*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	return cmd
}
