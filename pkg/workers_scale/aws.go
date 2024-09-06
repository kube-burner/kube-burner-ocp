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
	"sync"

	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/cloud-bulldozer/go-commons/indexers"
	"github.com/kube-burner/kube-burner/pkg/measurements"
	mtypes "github.com/kube-burner/kube-burner/pkg/measurements/types"
	mmetrics "github.com/kube-burner/kube-burner/pkg/measurements/metrics"
	log "github.com/sirupsen/logrus"
	
	machinev1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
)

type AWSScenario struct {}

// Returns a new scenario object
func (awsScenario *AWSScenario) OrchestrateWorkload(uuid string, additionalWorkerNodes int, metadata map[string]interface{}, indexerValue indexers.Indexer) {
	var err error
	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, restConfig := kubeClientProvider.ClientSet(0, 0)
	machineClient := getMachineClient(restConfig)
	machineSetDetails := getMachineSets(machineClient)
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
	machineSetsToEdit := adjustMachineSets(machineClient, machineSetDetails, additionalWorkerNodes)
	editMachineSets(machineClient, clientSet, machineSetsToEdit, true)
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
	editMachineSets(machineClient, clientSet, machineSetsToEdit, false)
}

// adjustMachineSets equally spreads requested number of machines across machinesets
func adjustMachineSets(machineClient *machinev1beta1.MachineV1beta1Client, machineSetReplicas map[int][]string, desiredWorkerCount int) (sync.Map){
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
	return machineSetsToEdit
}
