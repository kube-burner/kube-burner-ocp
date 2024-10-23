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
	"sync"
	"time"

	machinev1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	infrav1 "sigs.k8s.io/cluster-api-provider-aws/v2/api/v1beta2"
	capiv1beta1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// listMachines lists all worker machines in the cluster
func getMachines(machineClient *machinev1beta1.MachineV1beta1Client, scaleEventEpoch int64) (map[string]MachineInfo, string) {
	var amiID string
	var machineReadyTimestamp time.Time
	machineDetails := make(map[string]MachineInfo)
	machines, err := machineClient.Machines(machineNamespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Fatalf("error listing machines: %s", err)
	}

	for _, machine := range machines.Items {
		if _, ok := machine.Labels["machine.openshift.io/cluster-api-machine-role"]; ok &&
			machine.Labels["machine.openshift.io/cluster-api-machine-role"] != "master" &&
			machine.Labels["machine.openshift.io/cluster-api-machine-role"] != "infra" &&
			machine.Labels["machine.openshift.io/cluster-api-machine-role"] != "workload" {
			if machine.Status.Phase != nil && *machine.Status.Phase == "Running" && machine.CreationTimestamp.Time.UTC().Unix() > scaleEventEpoch {
				if amiID == "" {
					var awsSpec AWSProviderSpec
					if err := json.Unmarshal(machine.Spec.ProviderSpec.Value.Raw, &awsSpec); err != nil {
						log.Errorf("error unmarshaling providerSpec: %v", err)
					}
					amiID = awsSpec.AMI.ID
				}
				rawProviderStatus := machine.Status.ProviderStatus.Raw
				var providerStatus ProviderStatus
				if err := json.Unmarshal(rawProviderStatus, &providerStatus); err != nil {
					log.Errorf("error unmarshaling providerStatus: %v", err)
				}
				for _, condition := range providerStatus.Conditions {
					if condition.Type == "MachineCreation" && condition.Status == "True" {
						machineReadyTimestamp = condition.LastTransitionTime.Time.UTC()
						break
					}
				}
				machineDetails[machine.Name] = MachineInfo{
					nodeUID:           string(machine.Status.NodeRef.UID),
					creationTimestamp: machine.CreationTimestamp.Time.UTC(),
					readyTimestamp:    machineReadyTimestamp,
				}
			}
		}
	}
	log.Debugf("Machines: %v with amiID: %v", machineDetails, amiID)
	return machineDetails, amiID
}

// getCapiMachines to fetch cluster api kind machines
func getCapiMachines(capiClient client.Client, scaleEventEpoch int64, clusterID string, namespace string) (map[string]MachineInfo, string) {
	var amiID string
	var machineReadyTimestamp time.Time
	machineDetails := make(map[string]MachineInfo)

	labelSelector := client.MatchingLabels{"cluster.x-k8s.io/cluster-name": clusterID}
	machines := &capiv1beta1.MachineList{}
	amiID, err := getAMIIDFromAWSMachineTemplates(capiClient, namespace)
	if err != nil {
		log.Errorf("error getting AMI ID from AWSMachineTemplates: %v", err)
	}
	if err := capiClient.List(context.TODO(), machines, client.InNamespace(namespace), labelSelector); err != nil {
		log.Fatalf("Failed to list CAPI machines: %v", err)
	}
	for _, machine := range machines.Items {
		if machine.Status.Phase == "Running" && machine.CreationTimestamp.Time.UTC().Unix() > scaleEventEpoch {
			machineReadyTimestamp = getCapiMachineReadyTimestamp(machine)
			machineDetails[machine.Name] = MachineInfo{
				nodeUID:           string(machine.Status.NodeRef.UID),
				creationTimestamp: machine.CreationTimestamp.Time.UTC(),
				readyTimestamp:    machineReadyTimestamp,
			}
		}
	}
	log.Debugf("Machines: %v with amiID: %v", machineDetails, amiID)
	return machineDetails, amiID
}

// getAMIIDFromAWSMachineTemplates lists AWSMachineTemplates and extracts the AMI ID (only for ROSA HCP)
func getAMIIDFromAWSMachineTemplates(capiClient client.Client, namespace string) (string, error) {
	awsMachineTemplateList := &infrav1.AWSMachineTemplateList{}
	if err := capiClient.List(context.TODO(), awsMachineTemplateList, &client.ListOptions{Namespace: namespace}); err != nil {
		return "", fmt.Errorf("error listing AWSMachineTemplates: %v", err)
	}

	for _, template := range awsMachineTemplateList.Items {
		amiID := template.Spec.Template.Spec.AMI.ID
		return *amiID, nil // Return the first found AMI ID (or you can modify this logic)
	}

	return "", fmt.Errorf("no AWSMachineTemplates found in namespace %s", namespace)
}

// Helper function to get the machine ready timestamp
func getCapiMachineReadyTimestamp(machine capiv1beta1.Machine) time.Time {
	for _, condition := range machine.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == corev1.ConditionTrue {
			return condition.LastTransitionTime.Time.UTC()
		}
	}
	return time.Time{}
}

// editMachineSets edits machinesets parallelly
func editMachineSets(machineClient *machinev1beta1.MachineV1beta1Client, clientSet kubernetes.Interface, machineSetsToEdit *sync.Map, isScaleUp bool) {
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
			err := updateMachineSetReplicas(machineClient, ms, int32(r), machineSetsToEdit)
			if err != nil {
				log.Errorf("Failed to edit MachineSet %s: %v", ms, err)
			}
		}(machineSet, replica)
		return true
	})
	wg.Wait()
	log.Infof("All the machinesets have been editted")
	if err := waitForNodes(clientSet); err != nil {
		log.Infof("Error waiting for nodes: %v", err)
	}
}

// updateMachineSetsReplicas updates machines replicas
func updateMachineSetReplicas(machineClient *machinev1beta1.MachineV1beta1Client, name string, newReplicaCount int32, machineSetsToEdit *sync.Map) error {
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

	err = waitForMachineSet(machineClient, name, newReplicaCount)
	if err != nil {
		return fmt.Errorf("timeout waiting for MachineSet %s to be ready: %v", name, err)
	}

	log.Infof("MachineSet %s updated to %d replicas", name, newReplicaCount)
	return nil
}

// getMachineSets lists all machinesets
func getMachineSets(machineClient *machinev1beta1.MachineV1beta1Client) map[int][]string {
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
func waitForMachineSet(machineClient *machinev1beta1.MachineV1beta1Client, name string, newReplicaCount int32) error {
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

// waitForWorkerMachineSets waits for all the worker machinesets in specific to be ready
func waitForWorkerMachineSets(machineClient *machinev1beta1.MachineV1beta1Client) error {
	return wait.PollUntilContextTimeout(context.TODO(), time.Second, maxWaitTimeout, true, func(_ context.Context) (done bool, err error) {
		// Get all MachineSets with the worker label
		labelSelector := metav1.ListOptions{
			LabelSelector: "hive.openshift.io/machine-pool=worker",
		}
		machineSets, err := machineClient.MachineSets(machineNamespace).List(context.TODO(), labelSelector)
		if err != nil {
			return false, err
		}
		for _, ms := range machineSets.Items {
			if ms.Status.Replicas != ms.Status.ReadyReplicas {
				log.Debugf("Waiting for MachineSet %s to reach %d replicas, currently %d ready", ms.Name, ms.Status.Replicas, ms.Status.ReadyReplicas)
				return false, nil
			}
		}
		log.Info("All worker MachineSets have reached desired replica count")
		return true, nil
	})
}

// waitForWorkerMachineSets waits for all the cluster-api type worker machinesets in specific to be ready
func waitForCAPIMachineSets(capiClient client.Client, clusterID string, namespace string) error {
	return wait.PollUntilContextTimeout(context.TODO(), time.Second, maxWaitTimeout, true, func(_ context.Context) (done bool, err error) {
		machineSetList := &capiv1beta1.MachineSetList{}
		err = capiClient.List(context.TODO(), machineSetList, &client.ListOptions{
			Namespace: namespace,
			LabelSelector: labels.SelectorFromSet(map[string]string{
				"cluster.x-k8s.io/cluster-name": clusterID,
			}),
		})
		if err != nil {
			return false, err
		}

		for _, ms := range machineSetList.Items {
			if ms.Status.Replicas != ms.Status.ReadyReplicas {
				log.Debugf("Waiting for MachineSet %s to reach %d replicas, currently %d ready", ms.Name, ms.Status.Replicas, ms.Status.ReadyReplicas)
				return false, nil
			}
		}

		log.Info("All worker MachineSets have reached desired replica count")
		return true, nil
	})
}
