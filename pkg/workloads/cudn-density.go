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
	"math"
	"os"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	kubeburnermeasurements "github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"k8s.io/client-go/dynamic"

	"github.com/kube-burner/kube-burner-ocp/pkg/measurements"
)

var cudnMeasurementFactoryMap = map[string]kubeburnermeasurements.NewMeasurementFactory{
	"cudnLatency": measurements.NewCudnLatencyMeasurementFactory,
}

// NewCudnDensity holds cudn-density workload
func NewCudnDensity(wh *workloads.WorkloadHelper) *cobra.Command {
	var churnPercent, churnCycles, iterations, namespacesPerCudn int
	var cudnChurnCycles, cudnChurnPercent int
	var l3, pprof bool
	var churnDelay, churnDuration, podReadyThreshold, pprofInterval, jobPause, cudnChurnDelay time.Duration
	var churnMode string
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "cudn-density",
		Short:        "Runs cudn-density workload with tiered cross-namespace communication",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			if iterations%namespacesPerCudn != 0 {
				log.Fatalf("iterations (%d) must be divisible by namespaces-per-cudn (%d)", iterations, namespacesPerCudn)
			}
			if churnMode == string(config.ChurnNamespaces) && (churnDuration > 0 || churnCycles > 0) {
				log.Fatal("churn-mode=namespaces is not supported for cudn-density: CUDN finalizers block namespace deletion. Use --churn-mode=objects instead")
			}
			if cudnChurnCycles > 0 {
				if cudnChurnPercent < 1 || cudnChurnPercent > 99 {
					log.Fatalf("cudn-churn-percent must be between 1 and 99, got %d", cudnChurnPercent)
				}
				numCudns := iterations / namespacesPerCudn
				numToChurn := int(math.Ceil(float64(cudnChurnPercent) * float64(numCudns) / 100.0))
				if numToChurn < 1 {
					log.Fatalf("cudn-churn-percent=%d with %d CUDNs results in 0 CUDNs to churn", cudnChurnPercent, numCudns)
				}
				if numToChurn >= numCudns {
					log.Fatalf("cudn-churn-percent=%d would churn all %d CUDNs; reduce the percentage", cudnChurnPercent, numCudns)
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
				log.Info("Pod churn is enabled")
			}
			if cudnChurnCycles > 0 {
				log.Info("CUDN churn is enabled")
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
			AdditionalVars["CUDN_CHURN_ENABLED"] = cudnChurnCycles > 0
			AdditionalVars["CLEANUP_ONLY"] = false
			AdditionalVars["CUDN_CHURN_RECREATE"] = false

			wh.SetMeasurements(cudnMeasurementFactoryMap)

			if cudnChurnCycles == 0 {
				rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
			} else {
				gcEnabled, _ := cmd.Root().PersistentFlags().GetBool("gc")
				AdditionalVars["GC"] = false

				// Phase 1: Setup + pod churn
				rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
				if rc != 0 {
					log.Error("Phase 1 setup failed, skipping CUDN churn and proceeding to cleanup")
				}

				// Phase 2: CUDN churn cycles (skipped if Phase 1 failed)
				if rc == 0 {
					kubeClientProvider := config.NewKubeClientProvider("", "")
					clientSet, restConfig := kubeClientProvider.ClientSet(0, 0)
					dynamicClient, err := dynamic.NewForConfig(restConfig)
					if err != nil {
						log.Fatalf("Failed to create dynamic client: %v", err)
					}
					deleter := newCudnChurnDeleter(clientSet, dynamicClient, iterations, namespacesPerCudn, cudnChurnPercent)
					churnStartIdx := deleter.cudnIndices[0]
					numToChurn := len(deleter.cudnIndices)
					nsStart := churnStartIdx * namespacesPerCudn
					nsCount := numToChurn * namespacesPerCudn

					AdditionalVars["CUDN_CHURN_RECREATE"] = true
					AdditionalVars["CUDN_CHURN_NS_START"] = nsStart
					AdditionalVars["CUDN_CHURN_NS_COUNT"] = nsCount
					AdditionalVars["CUDN_CHURN_CUDN_START"] = churnStartIdx
					AdditionalVars["CUDN_CHURN_CUDN_COUNT"] = numToChurn

					for cycle := 0; cycle < cudnChurnCycles; cycle++ {
						log.Infof("CUDN churn cycle %d/%d: churning %d CUDNs (indices %d-%d)",
							cycle+1, cudnChurnCycles, numToChurn, churnStartIdx, churnStartIdx+numToChurn-1)

						if err := deleter.deleteAndWait(); err != nil {
							log.Errorf("CUDN churn cycle %d: %v", cycle+1, err)
							rc = 1
							break
						}

						wh.SetVariables(AdditionalVars, SetVars)
						churnRC := wh.Run(cmd.Name() + ".yml")
						if churnRC != 0 {
							log.Errorf("CUDN churn cycle %d recreation failed", cycle+1)
							rc = churnRC
							break
						}

						log.Infof("CUDN churn cycle %d/%d completed successfully", cycle+1, cudnChurnCycles)
						if cycle < cudnChurnCycles-1 {
							log.Infof("Sleeping %v before next CUDN churn cycle", cudnChurnDelay)
							time.Sleep(cudnChurnDelay)
						}
					}
					AdditionalVars["CUDN_CHURN_RECREATE"] = false
				}

				if rc != 0 {
					log.Error("CUDN churn failed, proceeding to cleanup")
				}

				// Phase 3: Cleanup
				if gcEnabled {
					AdditionalVars["CLEANUP_ONLY"] = true
					wh.SetVariables(AdditionalVars, SetVars)
					cleanupRC := wh.Run(cmd.Name() + ".yml")
					if cleanupRC != 0 && rc == 0 {
						rc = cleanupRC
					}
				}
			}
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
	cmd.Flags().StringVar(&churnMode, "churn-mode", string(config.ChurnObjects), "Churn mode: objects (namespaces mode is not supported due to CUDN finalizer constraints)")
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Total number of namespaces to create")
	cmd.Flags().IntVar(&namespacesPerCudn, "namespaces-per-cudn", 5, "Number of namespaces sharing the same CUDN")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 0, "Pod ready timeout threshold")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().IntVar(&cudnChurnCycles, "cudn-churn-cycles", 0, "Number of CUDN churn cycles (0=disabled). Each cycle deletes and recreates a percentage of CUDNs with their namespaces and workloads")
	cmd.Flags().IntVar(&cudnChurnPercent, "cudn-churn-percent", 10, "Percentage of CUDNs to churn per cycle (1-99)")
	cmd.Flags().DurationVar(&cudnChurnDelay, "cudn-churn-delay", 2*time.Minute, "Time to wait between CUDN churn cycles")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
