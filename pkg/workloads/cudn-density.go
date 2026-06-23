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
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	kubeburnermeasurements "github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kube-burner/kube-burner-ocp/pkg/measurements"
)

var cudnMeasurementFactoryMap = map[string]kubeburnermeasurements.NewMeasurementFactory{
	"cudnLatency": measurements.NewCudnLatencyMeasurementFactory,
}

// getDefaultGatewayIP reads the cluster's default gateway IP from the
// k8s.ovn.org/l3-gateway-config annotation on a worker node.
func getDefaultGatewayIP() string {
	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, _ := kubeClientProvider.ClientSet(0, 0)
	nodes, err := clientSet.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
		LabelSelector: "node-role.kubernetes.io/worker",
	})
	if err != nil {
		log.Fatalf("Error listing worker nodes: %v", err)
	}
	for _, node := range nodes.Items {
		gwConfig, exists := node.Annotations["k8s.ovn.org/l3-gateway-config"]
		if !exists {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(gwConfig), &parsed); err != nil {
			log.Warnf("Failed to parse l3-gateway-config on node %s: %v", node.Name, err)
			continue
		}
		defaultGW, ok := parsed["default"].(map[string]any)
		if !ok {
			continue
		}
		nextHop, ok := defaultGW["next-hop"].(string)
		if !ok || nextHop == "" {
			continue
		}
		log.Infof("Detected cluster default gateway IP: %s (from node %s)", nextHop, node.Name)
		return nextHop
	}
	log.Fatal("Unable to detect default gateway IP: no worker node has the k8s.ovn.org/l3-gateway-config annotation with a valid next-hop")
	return ""
}

// NewCudnDensity holds cudn-density workload
func NewCudnDensity(wh *workloads.WorkloadHelper) *cobra.Command {
	var churnPercent, churnCycles, iterations, namespacesPerCudn, incrementalStepSize int
	var incrementalExpBase float64
	var l3, pprof, gatewayCheck bool
	var churnDelay, churnDuration, podReadyThreshold, pprofInterval, jobPause, incrementalStepDelay time.Duration
	var churnMode, incrementalPattern string
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "cudn-density",
		Short:        "Runs cudn-density workload with tiered cross-namespace communication",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			if namespacesPerCudn < 1 {
				log.Fatal("--namespaces-per-cudn must be >= 1")
			}
			if iterations%namespacesPerCudn != 0 {
				log.Fatalf("iterations (%d) must be divisible by namespaces-per-cudn (%d)", iterations, namespacesPerCudn)
			}
			if churnMode != string(config.ChurnObjects) && churnMode != string(config.ChurnNamespaces) {
				log.Fatalf("--churn-mode must be 'objects' or 'namespaces', got '%s'", churnMode)
			}
			if incrementalStepSize > 0 {
				if incrementalStepSize > iterations {
					log.Fatalf("incremental-step-size (%d) must be <= iterations (%d)", incrementalStepSize, iterations)
				}
				if incrementalPattern != "linear" && incrementalPattern != "exponential" {
					log.Fatalf("incremental-pattern must be 'linear' or 'exponential', got '%s'", incrementalPattern)
				}
				if incrementalStepSize%namespacesPerCudn != 0 {
					log.Fatalf("incremental-step-size (%d) must be divisible by namespaces-per-cudn (%d)", incrementalStepSize, namespacesPerCudn)
				}
				if incrementalPattern == "exponential" && incrementalExpBase <= 1.0 {
					log.Fatalf("incremental-exp-base must be > 1.0, got %f", incrementalExpBase)
				}
				if churnDuration > 0 || churnCycles > 0 {
					log.Fatal("incremental load and churn cannot be used together")
				}
			}
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			if l3 {
				log.Info("Layer 3 topology enabled")
			} else {
				log.Info("Layer 2 topology enabled")
			}
			if churnDuration > 0 || churnCycles > 0 {
				log.Infof("Churn is enabled (mode: %s)", churnMode)
			}
			if incrementalStepSize > 0 {
				log.Infof("Incremental load enabled: pattern %s, step size %d namespaces (%d CUDNs), delay %v",
					incrementalPattern, incrementalStepSize, incrementalStepSize/namespacesPerCudn, incrementalStepDelay)
			}

			AdditionalVars["PPROF"] = pprof
			AdditionalVars["PPROF_INTERVAL"] = pprofInterval.String()
			AdditionalVars["JOB_PAUSE"] = jobPause
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["CHURN_MODE"] = churnMode
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["NAMESPACES_PER_CUDN"] = namespacesPerCudn
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["ENABLE_LAYER_3"] = l3
			AdditionalVars["INCREMENTAL_STEP_SIZE"] = incrementalStepSize
			AdditionalVars["INCREMENTAL_STEP_DELAY"] = incrementalStepDelay
			AdditionalVars["INCREMENTAL_PATTERN"] = incrementalPattern
			AdditionalVars["INCREMENTAL_EXP_BASE"] = incrementalExpBase
			AdditionalVars["GATEWAY_CHECK"] = gatewayCheck
			if gatewayCheck {
				gatewayIP := getDefaultGatewayIP()
				AdditionalVars["GATEWAY_IP"] = gatewayIP
			}
			wh.SetMeasurements(cudnMeasurementFactoryMap)
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().BoolVar(&l3, "layer3", false, "Use Layer3 topology instead of Layer2")
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Enable pprof collection for ovnkube components")
	cmd.Flags().DurationVar(&pprofInterval, "pprof-interval", 0, "Interval between pprof collections")
	cmd.Flags().DurationVar(&jobPause, "job-pause", 1*time.Minute, "Pause after CUDN creation to allow OVN-K network settling before workload deployment")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 0, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&churnMode, "churn-mode", string(config.ChurnObjects), "Churn mode: 'objects' churns deployments, 'namespaces' churns entire CUDN groups (CUDN + namespaces + pods)")
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Total number of namespaces to create")
	cmd.Flags().IntVar(&namespacesPerCudn, "namespaces-per-cudn", 5, "Number of namespaces sharing the same CUDN")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 0, "Pod ready timeout threshold")
	cmd.Flags().IntVar(&incrementalStepSize, "incremental-step-size", 0, "Namespaces to add per incremental step (0=disabled). Must be divisible by namespaces-per-cudn")
	cmd.Flags().DurationVar(&incrementalStepDelay, "incremental-step-delay", 5*time.Minute, "Delay between incremental load steps")
	cmd.Flags().StringVar(&incrementalPattern, "incremental-pattern", "linear", "Incremental load pattern: linear or exponential")
	cmd.Flags().Float64Var(&incrementalExpBase, "incremental-exp-base", 2.0, "Base for exponential incremental pattern (must be > 1.0)")
	cmd.Flags().BoolVar(&gatewayCheck, "gateway-check", false, "Enable default gateway reachability check from each namespace")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
