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

package workerscale

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/measurements"
	machinev1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RosaScenario struct{}

func (rosaScenario *RosaScenario) OrchestrateWorkload(scaleConfig ScaleConfig) {
	var err error
	var triggerJob string
	var clusterID string
	var hcNamespace string
	var machineClient interface{}
	var triggerTime time.Time

	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, restConfig := kubeClientProvider.ClientSet(0, 0)
	dynamicClient := dynamic.NewForConfigOrDie(restConfig)
	clusterID = getClusterID(dynamicClient, scaleConfig.IsHCP)
	if scaleConfig.IsHCP {
		if scaleConfig.MCKubeConfig == "" {
			log.Fatal("Error reading management cluster kubeconfig. Please provide a valid path")
		}
		mcKubeClientProvider := config.NewKubeClientProvider(scaleConfig.MCKubeConfig, "")
		mcClientSet, mcRestConfig := mcKubeClientProvider.ClientSet(0, 0)
		machineClient = getCAPIClient(mcRestConfig)
		hcNamespace = getHCNamespace(mcClientSet, clusterID)
	} else {
		machineClient = getMachineClient(restConfig)
	}

	if scaleConfig.ScaleEventEpoch != 0 && !scaleConfig.AutoScalerEnabled {
		log.Info("Scale event epoch time specified. Hence calculating node latencies without any scaling")
		setupMetrics(scaleConfig.UUID, scaleConfig.Metadata, kubeClientProvider)
		measurements.Start()
		if err = waitForNodes(clientSet); err != nil {
			log.Fatalf("Error waiting for nodes: %v", err)
		}
		scaledMachineDetails, amiID := getMachineDetails(machineClient, scaleConfig.ScaleEventEpoch, clusterID, hcNamespace, scaleConfig.IsHCP)
		if err := measurements.Stop(); err != nil {
			log.Error(err.Error())
		}
		finalizeMetrics(&sync.Map{}, scaledMachineDetails, scaleConfig.Indexer, amiID, scaleConfig.ScaleEventEpoch)
	} else {
		prevMachineDetails, _ := getMachineDetails(machineClient, 0, clusterID, hcNamespace, scaleConfig.IsHCP)
		setupMetrics(scaleConfig.UUID, scaleConfig.Metadata, kubeClientProvider)
		measurements.Start()

		triggerTime = editMachinepool(clusterID, len(prevMachineDetails), len(prevMachineDetails)+scaleConfig.AdditionalWorkerNodes, scaleConfig.AutoScalerEnabled, scaleConfig.IsHCP)
		if scaleConfig.AutoScalerEnabled {
			triggerJob, triggerTime = createBatchJob(clientSet)
			// Slightly more delay for the cluster autoscaler resources to come up
			time.Sleep(2 * time.Minute)
		}
		log.Info("Waiting for the machinesets to be ready")
		if err = waitForWorkers(machineClient, clusterID, hcNamespace, scaleConfig.IsHCP); err != nil {
			log.Fatalf("Error waiting for MachineSets to be ready: %v", err)
		}
		scaledMachineDetails, amiID := getMachineDetails(machineClient, 0, clusterID, hcNamespace, scaleConfig.IsHCP)
		discardPreviousMachines(prevMachineDetails, scaledMachineDetails)
		if err := measurements.Stop(); err != nil {
			log.Error(err.Error())
		}
		finalizeMetrics(&sync.Map{}, scaledMachineDetails, scaleConfig.Indexer, amiID, triggerTime.Unix())
		if scaleConfig.GC {
			log.Info("Restoring machine pool to previous state")
			editMachinepool(clusterID, len(prevMachineDetails), len(prevMachineDetails), scaleConfig.AutoScalerEnabled, scaleConfig.IsHCP)
			if scaleConfig.AutoScalerEnabled {
				deleteBatchJob(clientSet, triggerJob)
				time.Sleep(2 * time.Minute)
			}
			log.Info("Waiting for the machinesets to scale down")
			if err = waitForWorkers(machineClient, clusterID, hcNamespace, scaleConfig.IsHCP); err != nil {
				log.Fatalf("Error waiting for MachineSets to scale down: %v", err)
			}
		}
	}
}

// editMachinepool edits machinepool to desired replica count
func editMachinepool(clusterID string, minReplicas int, maxReplicas int, autoScalerEnabled bool, mcPresence bool) time.Time {
	verifyRosaInstall()
	var machinePoolName string
	triggerTime := time.Now().UTC().Truncate(time.Second)
	if mcPresence {
		machinePoolName = "workers"
	} else {
		machinePoolName = "worker"
	}
	cmdArgs := []string{"edit", "machinepool", "-c", clusterID, machinePoolName, fmt.Sprintf("--enable-autoscaling=%t", autoScalerEnabled)}
	if autoScalerEnabled {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--min-replicas=%d", minReplicas))
		cmdArgs = append(cmdArgs, fmt.Sprintf("--max-replicas=%d", maxReplicas))
	} else {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--replicas=%d", maxReplicas))
	}
	cmd := exec.Command("rosa", cmdArgs...)
	// Pass the current environment to the command
	cmd.Env = os.Environ()
	editOutput, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("Failed to edit machinepool: %v. Output: %s", err, string(editOutput))
	}
	log.Infof("Machinepool edited successfully on cluster: %v", clusterID)
	time.Sleep(1 * time.Minute)
	log.Debug(string(editOutput))
	return triggerTime
}

// verifyRosaInstall verifies rosa installation and login
func verifyRosaInstall() {
	if _, err := exec.LookPath("rosa"); err != nil {
		log.Fatal("ROSA CLI is not installed. Please install it and retry.")
		return
	}
	log.Info("ROSA CLI is installed.")

	cmd := exec.Command("rosa", "whoami")
	// Pass the current environment to the command
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatal("You are not logged in. Please login using 'rosa login' and retry.")
	}
	log.Info("You are already logged in.")
	log.Debug(string(output))
}

// getClusterID fetches the clusterID
func getClusterID(dynamicClient dynamic.Interface, mcPrescence bool) string {
	clusterVersionGVR := schema.GroupVersionResource{
		Group:    "config.openshift.io",
		Version:  "v1",
		Resource: "clusterversions",
	}

	clusterVersion, err := dynamicClient.Resource(clusterVersionGVR).Get(context.TODO(), "version", metav1.GetOptions{})
	if err != nil {
		log.Fatalf("Error fetching cluster version: %v", err)
	}

	clusterID, found, err := unstructured.NestedString(clusterVersion.Object, "spec", "clusterID")
	if err != nil || !found {
		log.Fatalf("Error retrieving cluster ID: %v", err)
	}

	// Special case for hcp where cluster version object has external ID
	if mcPrescence {
		cmd := exec.Command("rosa", "describe", "cluster", "-c", clusterID, "-o", "json")
		// Pass the current environment to the command
		cmd.Env = os.Environ()
		output, err := cmd.CombinedOutput()
		if err != nil {
			log.Fatalf("Failed to describe cluster: %v", err)
		}
		var result map[string]interface{}
		if err := json.Unmarshal(output, &result); err != nil {
			log.Fatalf("Failed to parse JSON: %v", err)
		}
		actualID, ok := result["id"].(string)
		if !ok {
			log.Fatal("ID field not found or invalid in the output.")
		}
		return actualID
	}
	return clusterID
}

// Function to fetch machine details based on the scenario (standard Rosa or RosaHCP).
func getMachineDetails(machineClient interface{}, epoch int64, clusterID string, hcNamespace string, isHCP bool) (map[string]MachineInfo, string) {
	if isHCP {
		return getCapiMachines(machineClient.(client.Client), epoch, clusterID, hcNamespace)
	}
	return getMachines(machineClient.(*machinev1beta1.MachineV1beta1Client), epoch)
}

// Function to wait for worker MachineSets based on the scenario (standard Rosa or RosaHCP).
func waitForWorkers(machineClient interface{}, clusterID string, hcNamespace string, isHCP bool) error {
	if isHCP {
		return waitForCAPIMachineSets(machineClient.(client.Client), clusterID, hcNamespace)
	}
	return waitForWorkerMachineSets(machineClient.(*machinev1beta1.MachineV1beta1Client))
}
