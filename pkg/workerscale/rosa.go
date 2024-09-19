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
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/measurements"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

type RosaScenario struct{}

// Returns a new scenario object
func (rosaScenario *RosaScenario) OrchestrateWorkload(scaleConfig ScaleConfig) {
	var err error
	var triggerJob string
	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, restConfig := kubeClientProvider.ClientSet(0, 0)
	machineClient := getMachineClient(restConfig)
	dynamicClient := dynamic.NewForConfigOrDie(restConfig)
	clusterID := getClusterID(dynamicClient)
	if scaleConfig.ScaleEventEpoch != 0 {
		log.Info("Scale event epoch time specified. Hence calculating node latencies without any scaling")
		setupMetrics(scaleConfig.UUID, scaleConfig.Metadata, kubeClientProvider)
		measurements.Start()
		if err := waitForNodes(clientSet); err != nil {
			log.Fatalf("Error waiting for nodes: %v", err)
		}
		if err = measurements.Stop(); err != nil {
			log.Error(err.Error())
		}
		scaledMachineDetails, amiID := getMachines(machineClient, scaleConfig.ScaleEventEpoch)
		finalizeMetrics(&sync.Map{}, scaledMachineDetails, scaleConfig.Indexer, amiID, scaleConfig.ScaleEventEpoch)
	} else {
		prevMachineDetails, _ := getMachines(machineClient, 0)
		setupMetrics(scaleConfig.UUID, scaleConfig.Metadata, kubeClientProvider)
		measurements.Start()
		log.Info("Updating machinepool to the desired worker count")
		triggerTime := editMachinepool(clusterID, len(prevMachineDetails), len(prevMachineDetails)+scaleConfig.AdditionalWorkerNodes, scaleConfig.AutoScalerEnabled)
		if scaleConfig.AutoScalerEnabled {
			triggerJob, triggerTime = createBatchJob(clientSet)
			// Delay for the clusterautoscaler resources to come up
			time.Sleep(5 * time.Minute)
		} else {
			// Delay for the rosa to update the machinesets
			time.Sleep(1 * time.Minute)
		}
		log.Info("Waiting for the machinesets to be ready")
		err = waitForWorkerMachineSets(machineClient)
		if err != nil {
			log.Fatalf("Error waitingMachineSets to be ready: %v", err)
		}
		if err := waitForNodes(clientSet); err != nil {
			log.Fatalf("Error waiting for nodes: %v", err)
		}
		if err = measurements.Stop(); err != nil {
			log.Error(err.Error())
		}
		scaledMachineDetails, amiID := getMachines(machineClient, 0)
		discardPreviousMachines(prevMachineDetails, scaledMachineDetails)
		finalizeMetrics(&sync.Map{}, scaledMachineDetails, scaleConfig.Indexer, amiID, triggerTime.Unix())
		if scaleConfig.GC {
			if scaleConfig.AutoScalerEnabled {
				deleteBatchJob(clientSet, triggerJob)
			}
			log.Info("Restoring machine sets to previous state")
			editMachinepool(clusterID, len(prevMachineDetails), len(prevMachineDetails), false)
		}
	}
}

// editMachinepool edits machinepool to desired replica count
func editMachinepool(clusterID string, minReplicas int, maxReplicas int, autoScalerEnabled bool) time.Time {
	verifyRosaInstall()
	triggerTime := time.Now().UTC().Truncate(time.Second)
	cmdArgs := []string{"edit", "machinepool", "-c", clusterID, "worker", fmt.Sprintf("--enable-autoscaling=%t", autoScalerEnabled)}
	if autoScalerEnabled {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--min-replicas=%d", minReplicas))
		cmdArgs = append(cmdArgs, fmt.Sprintf("--max-replicas=%d", maxReplicas))
	} else {
		cmdArgs = append(cmdArgs, fmt.Sprintf("--replicas=%d", minReplicas))
	}
	cmd := exec.Command("rosa", cmdArgs...)
	editOutput, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("Failed to edit machinepool: %v. Output: %s", err, string(editOutput))
	}
	log.Infof("Machinepool edited successfully on cluster: %v", clusterID)
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
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatal("You are not logged in. Please login using 'rosa login' and retry.")
	}
	log.Info("You are already logged in.")
	log.Debug(string(output))
}

// getClusterID fetches the clusterID
func getClusterID(dynamicClient dynamic.Interface) string {
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

	return clusterID
}
