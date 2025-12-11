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
	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

// NewOLMv1 holds OLMv1 workload
func NewOLMv1(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations int
	var catalogImage, deletionStrategy, namespace, prefixPkgName, prefixImgName string
	var metricsProfiles []string
	var rc, iterationsPerNamespace, churnCycles, churnPercent int
	var pprof, namespacedIterations, churn bool
	var churnDuration, churnDelay, jobIterationDelay, namespaceDelay time.Duration

	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		Run: func(cmd *cobra.Command, args []string) {
			kubeClientProvider := config.NewKubeClientProvider("", "")
			clientSet, _ := kubeClientProvider.ClientSet(0, 0)
			if err := clusterhealth.IsOLMv1Enabled(clientSet); err != nil {
				log.Fatal(err.Error())
			}
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["CATALOG_IMAGE"] = catalogImage
			AdditionalVars["PPROF"] = pprof
			AdditionalVars["NAMESPACED_ITERATIONS"] = namespacedIterations
			AdditionalVars["ITERATIONS_PER_NAMESPACE"] = iterationsPerNamespace
			AdditionalVars["CHURN"] = churn
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["NAMESPACE"] = namespace
			AdditionalVars["PREFIX_PKG_NAME_V1"] = prefixPkgName
			AdditionalVars["PREFIX_IMG_NAME"] = prefixImgName
			AdditionalVars["JOB_ITERATION_DELAY"] = jobIterationDelay
			AdditionalVars["NAMESPACE_DELAY"] = namespaceDelay

			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 10, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().BoolVar(&namespacedIterations, "namespaced-iterations", false, "Namespaced iterations")
	cmd.Flags().BoolVar(&churn, "churn", false, "Enable churning")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 5, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 1*time.Hour, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().StringVar(&deletionStrategy, "churn-deletion-strategy", "gvr", "Churn deletion strategy to use")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 20, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().IntVar(&iterationsPerNamespace, "iterations-per-namespace", 10, "Iterations per namespace")
	cmd.Flags().DurationVar(&jobIterationDelay, "job-iteration-delay", 0, "Delay between job iterations")
	cmd.Flags().DurationVar(&namespaceDelay, "namespace-delay", 0, "Delay after completing all iterations in a namespace before starting the next namespace")
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Enable pprof collection")
	cmd.Flags().StringVar(&catalogImage, "catalogImage", "registry.redhat.io/redhat/redhat-operator-index:v4.18", "the ClusterCatalog ref image")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics-aggregated.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().StringVar(&namespace, "namespace", "olmv1-ce", "Namespace to run the workload in")
	cmd.Flags().StringVar(&prefixPkgName, "prefix-pkg-name", "stress-olmv1-c", "Prefix for package names")
	cmd.Flags().StringVar(&prefixImgName, "prefix-image-name", "quay.io/olmqe/stress-index:vokv", "Prefix for catalog image names")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
