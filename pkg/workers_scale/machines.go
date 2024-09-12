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
	"fmt"
	"time"
	"sync"
	"context"
	"encoding/json"

	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	machinev1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
)

// listMachines lists all worker machines in the cluster
func getMachines(machineClient *machinev1beta1.MachineV1beta1Client) (map[string]MachineInfo, string) {
	var amiID string
	var machineReadyTimestamp time.Time
	machineDetails := make(map[string]MachineInfo)
	machines, err := machineClient.Machines(machineNamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Fatalf("error listing machines: %s", err)
	}

	for _, machine := range machines.Items {
		if _, ok := machine.Labels["machine.openshift.io/cluster-api-machine-role"]; ok && 
		machine.Labels["machine.openshift.io/cluster-api-machine-role"] != "infra" &&
		machine.Labels["machine.openshift.io/cluster-api-machine-role"] != "workload" &&
		machine.Labels["machine.openshift.io/cluster-api-machine-role"] != "master" &&
		machine.Labels["machine.openshift.io/cluster-api-machine-role"] == "worker" {
			if machine.Status.Phase != nil && *machine.Status.Phase == "Running" {
				if (amiID == "") {
					var awsSpec AWSProviderSpec
					if err := json.Unmarshal(machine.Spec.ProviderSpec.Value.Raw, &awsSpec); err != nil {
						log.Errorf("error unmarshaling providerSpec: %w", err)
					}
					amiID = awsSpec.AMI.ID
				}
				rawProviderStatus := machine.Status.ProviderStatus.Raw
				var providerStatus ProviderStatus
				if err := json.Unmarshal(rawProviderStatus, &providerStatus); err != nil {
					log.Errorf("error unmarshaling providerStatus: %w", err)
				}
				for _, condition := range providerStatus.Conditions {
					if condition.Type == "MachineCreation" && condition.Status == "True" {
						machineReadyTimestamp = condition.LastTransitionTime.Time.UTC()
						break
					}
				}
				machineDetails[machine.Name] = MachineInfo{
					nodeUID: string(machine.Status.NodeRef.UID),
					creationTimestamp: machine.CreationTimestamp.Time.UTC(),
					readyTimestamp: machineReadyTimestamp,
				}
			}
		}
	}
	log.Debugf("Machines: %v with amiID: %v", machineDetails, amiID)
	return machineDetails, amiID
}

// editMachineSets edits machinesets parallely
func editMachineSets(machineClient *machinev1beta1.MachineV1beta1Client, clientSet kubernetes.Interface, machineSetsToEdit sync.Map, isScaleUp bool) {
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
	if err := waitForNodes(clientSet, maxWaitTimeout); err != nil {
		log.Infof("Error waiting for nodes: %v", err)
	}
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

// getMachineSets lists all machinesets
func getMachineSets(machineClient *machinev1beta1.MachineV1beta1Client) (map[int][]string) {
	machineSetReplicas := make(map[int][]string)
	machineSets, err := machineClient.MachineSets(machineNamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Fatalf("error listing machinesets: %s", err)
	}

	for _, ms := range machineSets.Items {
		if ms.Labels["machine.openshift.io/cluster-api-machine-role"] != "infra" &&
		ms.Labels["machine.openshift.io/cluster-api-machine-role"] != "workload" {
			replicas := int(*ms.Spec.Replicas)
			machineSetReplicas[replicas] = append(machineSetReplicas[replicas], ms.Name)
		}
	}
	log.Debugf("MachineSets with replica count: %v", machineSetReplicas)
	return machineSetReplicas
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