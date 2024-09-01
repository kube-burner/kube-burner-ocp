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

package workers_scale


import (
	"sort"
	"context"
	"sync"
	"time"
	"fmt"

	"k8s.io/apimachinery/pkg/util/wait"
	"github.com/cloud-bulldozer/go-commons/indexers"
	"github.com/kube-burner/kube-burner/pkg/measurements"
	mtypes "github.com/kube-burner/kube-burner/pkg/measurements/types"
	mmetrics "github.com/kube-burner/kube-burner/pkg/measurements/metrics"
	log "github.com/sirupsen/logrus"
	"github.com/kube-burner/kube-burner/pkg/config"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	machinev1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
)

type AWSScenario struct {}

// Returns a new scenario object
func (awsScenario *AWSScenario) OrchestrateWorkload(uuid string, additionalWorkerNodes int, metadata map[string]interface{}, indexerValue indexers.Indexer) {
	var err error
	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, restConfig := kubeClientProvider.ClientSet(0, 0)
	machineClient := getMachineClient(restConfig)
	machineSetDetails := getWorkerMachineSets(machineClient)
	prevMachineDetails, _ := getMachines(machineClient)
	configSpec := config.Spec{
		GlobalConfig: config.GlobalConfig {
			UUID: uuid,
			Measurements: []mtypes.Measurement {
				{
					Name: "nodeLatency",
				},
			},
		},
	}
	measurements.NewMeasurementFactory(configSpec, metadata)
	measurements.SetJobConfig(
		&config.Job{
			Name: JobName,
		},
		kubeClientProvider,
	)
	measurements.Start()
	machineSetsToEdit := scaleMachineSets(machineClient, machineSetDetails, additionalWorkerNodes)
	if err = waitForNodes(clientSet, maxWaitTimeout); err != nil {
		log.Infof("Error waiting for nodes: %v", err)
	} else {
		log.Infof("All nodes are ready")
	}
	if err = measurements.Stop(); err != nil {
		log.Error(err.Error())
	}
	scaledMachineDetails, amiID := getMachines(machineClient)
	for key := range scaledMachineDetails {
		if _, exists := prevMachineDetails[key]; exists {
			delete(scaledMachineDetails, key)
		}
	}
	nodeMetrics := measurements.GetMetrics()
	normLatencies, latencyQuantiles := calculateMetrics(machineSetsToEdit, scaledMachineDetails, nodeMetrics[0], amiID)
	for _, q := range latencyQuantiles {
		nq := q.(mmetrics.LatencyQuantiles)
		log.Infof("%s: %s 50th: %v 99th: %v max: %v avg: %v", JobName, nq.QuantileName, nq.P50, nq.P99, nq.Max, nq.Avg)
	}
	metricMap := map[string][]interface{}{
		nodeReadyLatencyMeasurement: normLatencies,
		nodeReadyLatencyQuantilesMeasurement: latencyQuantiles,
	}
	measurements.IndexLatencyMeasurement(configSpec.GlobalConfig.Measurements[0], JobName, metricMap, map[string]indexers.Indexer{
		"": indexerValue,
	})
	log.Infof("Restoring machine sets to previous state")
	editMachineSets(machineClient, machineSetsToEdit, false)
}

// scaleMachineSets triggers scale operation on the machinesets
func scaleMachineSets(machineClient *machinev1beta1.MachineV1beta1Client, machineSetReplicas map[int][]string, desiredWorkerCount int) (sync.Map){
	var lastIndex int
	machineSetsToEdit := sync.Map{}
	var keys []int
	for key := range machineSetReplicas {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	index := 0
	for index < len(keys) {
		modified := false
		value := keys[index]
		if machineSets, exists := machineSetReplicas[value]; exists {
			for index, machineSet := range machineSets {
				if desiredWorkerCount > 0 {
					if _, exists := machineSetsToEdit.Load(machineSet); !exists {
						machineSetsToEdit.Store(machineSet, MachineSetInfo{
							prevReplicas: value,
							currentReplicas: value + 1,
						})
					}
					msValue, _ := machineSetsToEdit.Load(machineSet)
					msInfo := msValue.(MachineSetInfo)
					msInfo.currentReplicas = value + 1
					machineSetsToEdit.Store(machineSet, msInfo)
					machineSetReplicas[value + 1] = append(machineSetReplicas[value + 1], machineSet)
					lastIndex = index
					desiredWorkerCount--
					modified = true
				} else {
					break
				}
			}
			if lastIndex == len(machineSets) - 1 {
				delete(machineSetReplicas, value)
			} else {
				machineSetReplicas[value] = machineSets[lastIndex + 1:]
			}
		}
		if modified && (index == len(keys) - 1) || (value + 1 != keys[index + 1]) {
			keys = append(keys[:index+1], append([]int{value + 1}, keys[index+1:]...)...)
		}
		if desiredWorkerCount <= 0 {
			break
		}
		index++
	}
	editMachineSets(machineClient, machineSetsToEdit, true)
	return machineSetsToEdit
}

// editMachineSets edits machinesets parallely
func editMachineSets(machineClient *machinev1beta1.MachineV1beta1Client, machineSetsToEdit sync.Map, isScaleUp bool) {
	var wg sync.WaitGroup
	machineSetsToEdit.Range(func(key, value interface{}) bool {
		machineSet := key.(string)
		msInfo := value.(MachineSetInfo)
		var replica int
		if isScaleUp {
			replica = msInfo.currentReplicas
		} else {
			replica = msInfo.prevReplicas
		}
		wg.Add(1)
		go func(ms string, r int) {
			defer wg.Done()
			err := updateMachineSetReplicas(machineClient, ms, int32(r), maxWaitTimeout, machineSetsToEdit)
            if err != nil {
                log.Errorf("Failed to edit MachineSet %s: %v", ms, err)
            }
		}(machineSet, replica)
		return true
	})
	wg.Wait()
	log.Infof("All the machinesets have been editted")
}

// updateMachineSetsReplicas updates machines replicas
func updateMachineSetReplicas(machineClient *machinev1beta1.MachineV1beta1Client, name string, newReplicaCount int32, maxWaitTimeout time.Duration, machineSetsToEdit sync.Map) error {
    machineSet, err := machineClient.MachineSets(machineNamespace).Get(context.TODO(), name, metav1.GetOptions{})
    if err != nil {
        return fmt.Errorf("error getting machineset: %s", err)
    }

    machineSet.Spec.Replicas = &newReplicaCount
	updateTimestamp := time.Now().UTC().Truncate(time.Second)
    _, err = machineClient.MachineSets(machineNamespace).Update(context.TODO(), machineSet, metav1.UpdateOptions{})
    if err != nil {
        return fmt.Errorf("error updating machineset: %s", err)
    }
	msValue, _ := machineSetsToEdit.Load(name)
	msInfo := msValue.(MachineSetInfo)
	msInfo.lastUpdatedTime = updateTimestamp
	machineSetsToEdit.Store(name, msInfo)

	err = waitForMachineSet(machineClient, name, newReplicaCount, maxWaitTimeout)
	if err != nil {
        return fmt.Errorf("timeout waiting for MachineSet %s to be ready: %v", name, err)
    }

    log.Infof("MachineSet %s updated to %d replicas", name, newReplicaCount)
	return nil
}

// waitForMachineSet waits for machinesets to be ready with new replica count
func waitForMachineSet(machineClient *machinev1beta1.MachineV1beta1Client, name string, newReplicaCount int32, maxWaitTimeout time.Duration) error {
	return wait.PollUntilContextTimeout(context.TODO(), time.Second, maxWaitTimeout, true, func(ctx context.Context) (done bool, err error) {
        ms, err := machineClient.MachineSets(machineNamespace).Get(context.TODO(), name, metav1.GetOptions{})
        if err != nil {
            return false, err
        }
        if ms.Status.Replicas == ms.Status.ReadyReplicas && ms.Status.ReadyReplicas == newReplicaCount {
            return true, nil
        }
        log.Debugf("Waiting for MachineSet %s to reach %d replicas, currently %d ready", name, newReplicaCount, ms.Status.ReadyReplicas)
        return false, nil
    })
}

