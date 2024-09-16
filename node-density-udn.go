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

package ocp

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"

	"github.com/spf13/cobra"
)

// NewNodeDensity holds node-density-cni workload
func NewNodeDensityUDN(wh *workloads.WorkloadHelper) *cobra.Command {
	var churnPercent, churnCycles, iterations, podsPerNode int
	var churn, svcLatency bool
	var churnDelay, churnDuration, podReadyThreshold time.Duration
	var churnDeletionStrategy string
	cmd := &cobra.Command{
		Use:          "node-density-udn",
		Short:        "Runs node-density-udn workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			os.Setenv("CHURN", fmt.Sprint(churn))
			os.Setenv("CHURN_CYCLES", fmt.Sprintf("%v", churnCycles))
			os.Setenv("CHURN_DURATION", fmt.Sprintf("%v", churnDuration))
			os.Setenv("CHURN_DELAY", fmt.Sprintf("%v", churnDelay))
			os.Setenv("CHURN_PERCENT", fmt.Sprint(churnPercent))
			os.Setenv("CHURN_DELETION_STRATEGY", churnDeletionStrategy)
			ingressDomain, err := wh.MetadataAgent.GetDefaultIngressDomain()
			if err != nil {
				log.Fatal("Error obtaining default ingress domain: ", err.Error())
			}
			os.Setenv("INGRESS_DOMAIN", ingressDomain)
			os.Setenv("JOB_ITERATIONS", fmt.Sprint(iterations))
			os.Setenv("POD_READY_THRESHOLD", fmt.Sprintf("%v", podReadyThreshold))
			os.Setenv("SVC_LATENCY", strconv.FormatBool(svcLatency))
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, "metrics.yml")
			wh.Run(cmd.Name())
		},
	}
	cmd.Flags().BoolVar(&churn, "churn", true, "Enable churning")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 1*time.Hour, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&churnDeletionStrategy, "churn-deletion-strategy", "default", "Churn deletion strategy to use")
	cmd.Flags().IntVar(&iterations, "iterations", 0, fmt.Sprintf("Iterations"))
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 1*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().IntVar(&podsPerNode, "pods-per-node", 245, "Pods per node")
	cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	return cmd
}
