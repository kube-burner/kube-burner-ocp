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
	"os/exec"
	"time"

	"github.com/cloud-bulldozer/go-commons/v2/ssh"
	"github.com/kube-burner/kube-burner/v2/pkg/config"
	kubeburnermeasurements "github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kube-burner/kube-burner-ocp/pkg/measurements"
)

const cudnDensitySSHKeyTmpDirPattern = "kube-burner-cudn-density-ssh"

// enableClusterBGP patches the OCP network operator to enable FRR and
// RouteAdvertisements, then polls until the CRD exists before kube-burner
// validates object templates.
func enableClusterBGP() {
	log.Info("Enabling FRR and RouteAdvertisements on the cluster...")
	patchJSON := `{"spec":{"additionalRoutingCapabilities":{"providers":["FRR"]},"defaultNetwork":{"ovnKubernetesConfig":{"routeAdvertisements":"Enabled"}}}}`
	cmd := exec.Command("oc", "patch", "Network.operator.openshift.io", "cluster", "--type=merge", "-p", patchJSON)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to patch Network operator for BGP: %v", err)
	}
	log.Info("Waiting for RouteAdvertisements CRD to appear...")
	deadline := time.Now().Add(5 * time.Minute)
	for {
		cmd = exec.Command("oc", "get", "crd", "routeadvertisements.k8s.ovn.org", "--no-headers")
		if err := cmd.Run(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			log.Fatal("Timed out waiting for RouteAdvertisements CRD to be created (5m)")
		}
		time.Sleep(5 * time.Second)
	}
	log.Info("Cluster BGP enabled, RouteAdvertisements CRD is available")
}

var cudnMeasurementFactoryMap = map[string]kubeburnermeasurements.NewMeasurementFactory{
	"cudnLatency":  measurements.NewCudnLatencyMeasurementFactory,
	"sshCheck":     measurements.NewSSHCheckMeasurementFactory,
	"sshLoadTest":  measurements.NewSSHLoadTestMeasurementFactory,
}

// getNodeGatewayMap builds a JSON mapping of nodeInternalIP -> gatewayIP by reading
// the k8s.ovn.org/l3-gateway-config annotation from all worker nodes.
// Returns e.g. {"10.0.1.5":"192.168.1.1","10.0.2.6":"192.168.2.1"}
func getNodeGatewayMap() string {
	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, _ := kubeClientProvider.ClientSet(0, 0)
	nodes, err := clientSet.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
		LabelSelector: "node-role.kubernetes.io/worker",
	})
	if err != nil {
		log.Fatalf("Error listing worker nodes: %v", err)
	}

	gwMap := make(map[string]string)

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

		var nodeIP string
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				nodeIP = addr.Address
				break
			}
		}
		if nodeIP == "" {
			log.Warnf("Node %s has no InternalIP address, skipping", node.Name)
			continue
		}

		log.Debugf("Node %s (IP: %s) -> gateway: %s", node.Name, nodeIP, nextHop)
		gwMap[nodeIP] = nextHop
	}

	if len(gwMap) == 0 {
		log.Fatal("Unable to detect gateway IPs: no worker node has the k8s.ovn.org/l3-gateway-config annotation with a valid next-hop")
	}

	jsonBytes, err := json.Marshal(gwMap)
	if err != nil {
		log.Fatalf("Error marshaling gateway map to JSON: %v", err)
	}
	return string(jsonBytes)
}

// NewCudnDensity holds cudn-density workload
func NewCudnDensity(wh *workloads.WorkloadHelper) *cobra.Command {
	var churnPercent, churnCycles, iterations, namespacesPerCudn, cudnsPerRA, incrementalStepSize int
	var incrementalExpBase float64
	var deletionStrategy string
	var l3, pprof, gatewayCheck, bgp, ocpbugs85627Workaround, sshLatencyCheck, sshLoadTest bool
	var churnDelay, churnDuration, podReadyThreshold, pprofInterval, jobPause, incrementalStepDelay, sshLoadTestDuration time.Duration
	var churnMode, incrementalPattern, externalHost string
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
			if cudnsPerRA < 1 {
				log.Fatal("--cudns-per-ra must be >= 1")
			}
			if cudnsPerRA > 1 && namespacesPerCudn > 1 {
				log.Fatal("kube-burner doesn't support different values for repeatEveryNIterations. So set --namespaces-per-cudn=1 if --cudns-per-ra >= 1")
			}
			if iterations%namespacesPerCudn != 0 {
				log.Fatalf("iterations (%d) must be divisible by namespaces-per-cudn (%d)", iterations, namespacesPerCudn)
			}
			if churnMode != string(config.ChurnObjects) && churnMode != string(config.ChurnNamespaces) {
				log.Fatalf("--churn-mode must be 'objects' or 'namespaces', got '%s'", churnMode)
			}
			if incrementalStepSize < 0 {
				log.Fatal("--incremental-step-size must be >= 0")
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
			if bgp && externalHost == "" {
				log.Fatal("--external-host is required when --bgp is enabled (needed for the BGP setup and NetworkPolicy rules)")
			}
			if sshLatencyCheck && !bgp {
				log.Fatal("--ssh-latency-check requires --bgp to be enabled")
			}
			if sshLoadTest && !bgp {
				log.Fatal("--ssh-load-test requires --bgp to be enabled")
			}
			if sshLoadTest && sshLoadTestDuration <= 0 {
				log.Fatal("--ssh-load-test-duration must be > 0 when --ssh-load-test is enabled")
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
			AdditionalVars["CUDNS_PER_RA"] = cudnsPerRA
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["ENABLE_LAYER_3"] = l3
			AdditionalVars["INCREMENTAL_STEP_SIZE"] = incrementalStepSize
			AdditionalVars["INCREMENTAL_STEP_DELAY"] = incrementalStepDelay
			AdditionalVars["INCREMENTAL_PATTERN"] = incrementalPattern
			AdditionalVars["INCREMENTAL_EXP_BASE"] = incrementalExpBase
			AdditionalVars["GATEWAY_CHECK"] = gatewayCheck
			AdditionalVars["BGP"] = bgp
			AdditionalVars["OCPBUGS_85627_WORKAROUND"] = ocpbugs85627Workaround
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["SSH_LATENCY_CHECK"] = sshLatencyCheck
			AdditionalVars["SSH_LOAD_TEST"] = sshLoadTest
			AdditionalVars["SSH_LOAD_TEST_DURATION"] = sshLoadTestDuration.String()
			AdditionalVars["SSH_LOAD_TEST_DURATION_SECS"] = int(sshLoadTestDuration.Seconds())
			if gatewayCheck {
				AdditionalVars["NODE_GW_MAP"] = getNodeGatewayMap()
			}
			if bgp {
				enableClusterBGP()
				privateKeyPath, publicKeyPath, err := ssh.GenerateSSHKeyPair("", cudnDensitySSHKeyTmpDirPattern, "ssh")
				if err != nil {
					log.Fatalf("Failed to generate SSH keys for the CUDN ssh check - %v", err)
				}
				AdditionalVars["privateKey"] = privateKeyPath
				AdditionalVars["publicKey"] = publicKeyPath
				AdditionalVars["EXTERNAL_HOST"] = externalHost
				AdditionalVars["EXTERNAL_HOST_CIDR"] = externalHost + "/32"
			}
			wh.SetMeasurements(cudnMeasurementFactoryMap)
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().BoolVar(&l3, "layer3", false, "Use Layer3 topology instead of Layer2")
	cmd.Flags().BoolVar(&bgp, "bgp", false, "Enable BGP route advertisement for each CUDN (requires external FRR on the default gatewaynode)")
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
	cmd.Flags().IntVar(&cudnsPerRA, "cudns-per-ra", 1, "How many CUDNs an RA exports")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 0, "Pod ready timeout threshold")
	cmd.Flags().IntVar(&incrementalStepSize, "incremental-step-size", 0, "Namespaces to add per incremental step (0=disabled). Must be divisible by namespaces-per-cudn")
	cmd.Flags().DurationVar(&incrementalStepDelay, "incremental-step-delay", 5*time.Minute, "Delay between incremental load steps")
	cmd.Flags().StringVar(&incrementalPattern, "incremental-pattern", "linear", "Incremental load pattern: linear or exponential")
	cmd.Flags().Float64Var(&incrementalExpBase, "incremental-exp-base", 2.0, "Base for exponential incremental pattern (must be > 1.0)")
	cmd.Flags().BoolVar(&gatewayCheck, "gateway-check", false, "Enable default gateway reachability check from each namespace")
	cmd.Flags().BoolVar(&ocpbugs85627Workaround, "static-mac-binding-workaround", false, "Deploy OCPBUGS-85627 static MAC binding workaround DaemonSet before creating CUDNs")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.DefaultDeletionStrategy, "GC deletion mode, default deletes entire namespaces and gvr deletes objects within namespaces before deleting the parent namespace")
	cmd.Flags().BoolVar(&sshLatencyCheck, "ssh-latency-check", false, "Enable external SSH latency check against CUDN server pods (requires --bgp)")
	cmd.Flags().BoolVar(&sshLoadTest, "ssh-load-test", false, "Enable SSH load test — parallel connections to all pods for the specified duration, reports success/fail count (requires --bgp). Runs after --ssh-latency-check if both are set")
	cmd.Flags().DurationVar(&sshLoadTestDuration, "ssh-load-test-duration", 5*time.Minute, "Duration to run the SSH load test")
	cmd.Flags().StringVar(&externalHost, "external-host", "", "IP address of the external host running the SSH check, used for BGP setup and NetworkPolicy rules. Required when --bgp is set")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
