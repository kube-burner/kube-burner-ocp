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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/kube-burner/kube-burner-ocp/pkg/clusterhealth"
	"github.com/kube-burner/kube-burner/v2/pkg/config"

	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// NewClusterDensity holds cluster-density workload
func NewClusterDensity(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, churnPercent, churnCycles int
	var svcLatency, pprof bool
	var churnDelay, churnDuration, pprofInterval time.Duration
	var deletionStrategy, churnMode, selector string
	var podReadyThreshold time.Duration
	var metricsProfiles []string
	var rc int
	var nodeSelector corev1.NodeSelector
	var matchExpressions []corev1.NodeSelectorRequirement
	const workerNodeSelector = "node-role.kubernetes.io/worker=,node-role.kubernetes.io/infra!=,node-role.kubernetes.io/workload!="
	cmd := &cobra.Command{
		Use:          variant,
		Short:        fmt.Sprintf("Runs %v workload", variant),
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			kubeClientProvider := config.NewKubeClientProvider("", "")
			clientSet, _ := kubeClientProvider.ClientSet(0, 0)
			if cmd.Name() == "cluster-density-v2" {
				if err := clusterhealth.IsClusterImageRegistryAvailable(clientSet); err != nil {
					log.Fatal(err.Error())
				}
			}
			labelSelector, err := labels.Parse(selector)
			if err != nil {
				log.Fatal(err.Error())
			}
			reqList, _ := labelSelector.Requirements()
			for _, req := range reqList {
				matchExpression := corev1.NodeSelectorRequirement{
					Key: req.Key(),
				}
				if req.Values().List()[0] == "" {
					if req.Operator() == "=" {
						matchExpression.Operator = corev1.NodeSelectorOpExists
					} else if req.Operator() == "!=" {
						matchExpression.Operator = corev1.NodeSelectorOpDoesNotExist
					}
				} else {
					matchExpression.Operator = corev1.NodeSelectorOpIn
					matchExpression.Values = req.Values().List()
				}
				matchExpressions = append(matchExpressions, matchExpression)
			}
			nodeSelector.NodeSelectorTerms = []corev1.NodeSelectorTerm{{MatchExpressions: matchExpressions}}
			nodeSelectorJson, err := json.Marshal(nodeSelector)
			if err != nil {
				log.Fatal(err.Error())
			}
			if !cmd.Flags().Changed("iterations") {
				nodes, err := clientSet.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{LabelSelector: selector})
				if err != nil {
					log.Fatal(err.Error())
				}
				if len(nodes.Items) == 0 {
					log.Fatalf("No nodes found with the selector: %s", selector)
				}
				iterations = len(nodes.Items)
				log.Infof("Auto-calculated %d iterations from %d node(s) matching selector %q", iterations, len(nodes.Items), selector)
			}
			setMetrics(cmd, metricsProfiles)
			ingressDomain := ""
			if clusterDensityNeedsIngressDomain(cmd.Name()) {
				var err error
				ingressDomain, err = wh.MetadataAgent.GetDefaultIngressDomain()
				if err != nil {
					log.Fatal("Error obtaining default ingress domain: ", err.Error())
				}
			}
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["NODE_SELECTOR"] = string(nodeSelectorJson)
			AdditionalVars["PPROF"] = pprof
			AdditionalVars["PPROF_INTERVAL"] = pprofInterval.String()
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["SVC_LATENCY"] = svcLatency
			AdditionalVars["INGRESS_DOMAIN"] = ingressDomain
			AdditionalVars["CHURN_MODE"] = churnMode

			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 0, "Pod ready timeout threshold")
	cmd.Flags().IntVar(&iterations, "iterations", 0, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Enable pprof collection")
	cmd.Flags().DurationVar(&pprofInterval, "pprof-interval", 0, "Interval between pprof collections")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 0, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().StringVar(&churnMode, "churn-mode", string(config.ChurnNamespaces), "Either namespaces, to churn entire namespaces or objects, to churn individual objects")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.DefaultDeletionStrategy, "GC deletion mode, default deletes entire namespaces and gvr deletes objects within namespaces before deleting the parent namespace")
	cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().StringVar(&selector, "selector", workerNodeSelector, "Node selector")
	return cmd
}

func clusterDensityNeedsIngressDomain(variant string) bool {
	return variant != "cluster-density-ms"
}
