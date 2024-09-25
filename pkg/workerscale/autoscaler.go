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
	"sync"
	"time"

	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/measurements"
	machinev1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
	log "github.com/sirupsen/logrus"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

type AutoScalerScenario struct{}

// Returns a new scenario object
func (awsAutoScalerScenario *AutoScalerScenario) OrchestrateWorkload(scaleConfig ScaleConfig) {
	var err error
	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, restConfig := kubeClientProvider.ClientSet(0, 0)
	dynamicClient := dynamic.NewForConfigOrDie(restConfig)
	machineClient := getMachineClient(restConfig)
	machineSetDetails := getMachineSets(machineClient)
	prevMachineDetails, _ := getMachines(machineClient, 0)
	machineSetsToEdit := adjustMachineSets(machineSetDetails, scaleConfig.AdditionalWorkerNodes)
	setupMetrics(scaleConfig.UUID, scaleConfig.Metadata, kubeClientProvider)
	measurements.Start()
	createMachineAutoscalers(dynamicClient, machineSetsToEdit)
	createAutoScaler(dynamicClient, len(prevMachineDetails)+scaleConfig.AdditionalWorkerNodes)
	triggerJob, triggerTime := createBatchJob(clientSet)
	// Delay for the clusterautoscaler resources to come up
	time.Sleep(5 * time.Minute)
	waitForMachineSets(machineClient, clientSet, machineSetsToEdit, triggerTime)
	if err = measurements.Stop(); err != nil {
		log.Error(err.Error())
	}
	scaledMachineDetails, amiID := getMachines(machineClient, 0)
	discardPreviousMachines(prevMachineDetails, scaledMachineDetails)
	finalizeMetrics(machineSetsToEdit, scaledMachineDetails, scaleConfig.Indexer, amiID, 0)
	deleteAutoScaler(dynamicClient)
	deleteMachineAutoscalers(dynamicClient, machineSetsToEdit)
	deleteBatchJob(clientSet, triggerJob)
	if scaleConfig.GC {
		log.Info("Restoring machine sets to previous state")
		editMachineSets(machineClient, clientSet, machineSetsToEdit, false)
	}
}

// createBatchJob creates a job to load the cluster
func createBatchJob(clientset kubernetes.Interface) (string, time.Time) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "work-queue-",
		},
		Spec: batchv1.JobSpec{
			Completions: int32Ptr(1000),
			Parallelism: int32Ptr(1000),
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:    "work",
							Image:   "quay.io/quay/busybox:latest",
							Command: []string{"sleep", "300"},
							Resources: v1.ResourceRequirements{
								Requests: v1.ResourceList{
									v1.ResourceMemory: resource.MustParse("1000Mi"),
									v1.ResourceCPU:    resource.MustParse("1000m"),
								},
							},
						},
					},
					RestartPolicy: v1.RestartPolicyNever,
				},
			},
			BackoffLimit: int32Ptr(4),
		},
	}

	jobsClient := clientset.BatchV1().Jobs(defaultNamespace)
	triggerTime := time.Now().UTC().Truncate(time.Second)
	createdJob, err := jobsClient.Create(context.TODO(), job, metav1.CreateOptions{})
	if err != nil {
		log.Fatalf("error creating Job: %s", err)
	}

	log.Infof("Job created: %s", createdJob.Name)
	return createdJob.Name, triggerTime
}

// deletes our batch job that creates load
func deleteBatchJob(clientset kubernetes.Interface, jobName string) {
	jobsClient := clientset.BatchV1().Jobs(defaultNamespace)
	deletePolicy := metav1.DeletePropagationForeground
	err := jobsClient.Delete(context.TODO(), jobName, metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	})
	if err != nil {
		if errors.IsNotFound(err) {
			log.Infof("Job %s not found in namespace %s", jobName, defaultNamespace)
			return
		}
		log.Fatalf("Error deleting Job %s: %v", jobName, err)
	}

	log.Infof("Job %s deleted successfully in namespace %s", jobName, defaultNamespace)
}

// createMachineAutoscalers will create the autoscalers at machine level
func createMachineAutoscalers(dynamicClient dynamic.Interface, machineSetsToEdit *sync.Map) {
	machineSetsToEdit.Range(func(key, value interface{}) bool {
		machineSet := key.(string)
		msInfo := value.(MachineSetInfo)
		gvr := schema.GroupVersionResource{
			Group:    "autoscaling.openshift.io",
			Version:  "v1beta1",
			Resource: "machineautoscalers",
		}

		machineAutoscaler := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "autoscaling.openshift.io/v1beta1",
				"kind":       "MachineAutoscaler",
				"metadata": map[string]interface{}{
					"name":      machineSet,
					"namespace": machineNamespace,
				},
				"spec": map[string]interface{}{
					"minReplicas": 0,
					"maxReplicas": msInfo.currentReplicas,
					"scaleTargetRef": map[string]interface{}{
						"apiVersion": "machine.openshift.io/v1beta1",
						"kind":       "MachineSet",
						"name":       machineSet,
					},
				},
			},
		}
		_, err := dynamicClient.Resource(gvr).Namespace(machineNamespace).Create(context.TODO(), machineAutoscaler, metav1.CreateOptions{})
		if err != nil {
			if errors.IsAlreadyExists(err) {
				log.Infof("machine autoscaler resource %s already exists", machineSet)
				return true
			} else {
				log.Fatalf("failed to create MachineAutoscaler: %v", err)
			}
		}

		log.Infof("MachineAutoscaler created: %v", machineSet)
		return true
	})
}

// deleteMachineAutoscalers deletes the MachineAutoscaler resources for the provided machine sets
func deleteMachineAutoscalers(dynamicClient dynamic.Interface, machineSetsToEdit *sync.Map) {
	machineSetsToEdit.Range(func(key, value interface{}) bool {
		machineSet := key.(string)

		// Define the GroupVersionResource for the MachineAutoscaler
		gvr := schema.GroupVersionResource{
			Group:    "autoscaling.openshift.io",
			Version:  "v1beta1",
			Resource: "machineautoscalers",
		}

		// Attempt to delete the MachineAutoscaler for the machineSet
		err := dynamicClient.Resource(gvr).Namespace(machineNamespace).Delete(context.TODO(), machineSet, metav1.DeleteOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				log.Infof("Machine Autoscaler %s not found", machineSet)
				return true
			} else {
				log.Fatalf("failed to delete MachineAutoscaler: %v", err)
			}
		}

		log.Infof("Machine Autoscaler %s deleted successfully", machineSet)
		return true
	})
}

// createAutoScaler creates the autoscaler resource on the cluster
func createAutoScaler(dynamicClient dynamic.Interface, maxNodesTotal int) {
	gvr := schema.GroupVersionResource{
		Group:    "autoscaling.openshift.io",
		Version:  "v1",
		Resource: "clusterautoscalers",
	}

	clusterAutoscaler := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "autoscaling.openshift.io/v1",
			"kind":       "ClusterAutoscaler",
			"metadata": map[string]interface{}{
				"name": defaultClusterAutoScaler,
			},
			"spec": map[string]interface{}{
				"podPriorityThreshold": -100,
				"resourceLimits": map[string]interface{}{
					"maxNodesTotal": maxNodesTotal,
				},
				"scaleDown": map[string]interface{}{
					"enabled": false,
				},
			},
		},
	}

	_, err := dynamicClient.Resource(gvr).Namespace("").Create(context.TODO(), clusterAutoscaler, metav1.CreateOptions{})
	if err != nil {
		if errors.IsAlreadyExists(err) {
			log.Infof("cluster autoscaler resource %s already exists", defaultClusterAutoScaler)
			return
		} else {
			log.Fatalf("failed to create ClusterAutoscaler: %v", err)
		}
	}

	log.Infof("Cluster Autoscaler created: %v", defaultClusterAutoScaler)
}

// deleteAutoScaler deletes the ClusterAutoscaler resource on the cluster by its name
func deleteAutoScaler(dynamicClient dynamic.Interface) {
	gvr := schema.GroupVersionResource{
		Group:    "autoscaling.openshift.io",
		Version:  "v1",
		Resource: "clusterautoscalers",
	}

	// Delete the ClusterAutoscaler
	err := dynamicClient.Resource(gvr).Namespace("").Delete(context.TODO(), defaultClusterAutoScaler, metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.Infof("Cluster Autoscaler %s not found", defaultClusterAutoScaler)
			return
		} else {
			log.Fatalf("failed to delete ClusterAutoscaler: %v", err)
		}
	}

	log.Infof("Cluster Autoscaler %s deleted successfully", defaultClusterAutoScaler)
}

// Wait for machinesets to get ready
func waitForMachineSets(machineClient *machinev1beta1.MachineV1beta1Client, clientSet kubernetes.Interface, machineSetsToEdit *sync.Map, triggerTime time.Time) {
	var wg sync.WaitGroup
	machineSetsToEdit.Range(func(key, value interface{}) bool {
		machineSet := key.(string)
		msInfo := value.(MachineSetInfo)
		msInfo.lastUpdatedTime = triggerTime
		machineSetsToEdit.Store(machineSet, msInfo)
		wg.Add(1)
		go func(ms string, r int) {
			defer wg.Done()
			err := waitForMachineSet(machineClient, ms, int32(r))
			if err != nil {
				log.Errorf("Failed waiting for MachineSet %s: %v", ms, err)
			}
		}(machineSet, msInfo.currentReplicas)
		return true
	})
	wg.Wait()
	log.Infof("All the machinesets have been scaled")
	if err := waitForNodes(clientSet); err != nil {
		log.Fatalf("Error waiting for nodes: %v", err)
	}
}
