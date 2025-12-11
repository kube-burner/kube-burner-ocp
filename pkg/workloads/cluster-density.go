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
	"fmt"
	"os"
	"time"

	"github.com/kube-burner/kube-burner-ocp/pkg/clusterhealth"
	"github.com/kube-burner/kube-burner/v2/pkg/config"

	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// NewClusterDensity holds cluster-density workload
func NewClusterDensity(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, churnPercent, churnCycles int
	var svcLatency, pprof bool
	var churnDelay, churnDuration time.Duration
	var deletionStrategy, churnMode string
	var podReadyThreshold time.Duration
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		Run: func(cmd *cobra.Command, args []string) {
			if cmd.Name() == "cluster-density-v2" {
				kubeClientProvider := config.NewKubeClientProvider("", "")
				clientSet, _ := kubeClientProvider.ClientSet(0, 0)
				if err := clusterhealth.IsClusterImageRegistryAvailable(clientSet); err != nil {
					log.Fatal(err.Error())
				}
			}
			setMetrics(cmd, metricsProfiles)
			ingressDomain, err := wh.MetadataAgent.GetDefaultIngressDomain()
			if err != nil {
				log.Fatal("Error obtaining default ingress domain: ", err.Error())
			}
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["PPROF"] = pprof
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["SVC_LATENCY"] = svcLatency
			AdditionalVars["INGRESS_DOMAIN"] = ingressDomain
			AdditionalVars["CHURN_MODE"] = churnMode

			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 2*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().IntVar(&iterations, "iterations", 0, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Enable pprof collection")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 0, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().StringVar(&churnMode, "churn-mode", string(config.ChurnNamespaces), "Either namespaces, to churn entire namespaces or objects, to churn individual objects")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.DefaultDeletionStrategy, "GC deletion mode, default deletes entire namespaces and gvr deletes objects within namespaces before deleting the parent namespace")
	cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics-aggregated.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
