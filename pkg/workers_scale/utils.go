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
	"time"
	"sync"
	"context"
	"strings"
	"encoding/json"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/kubernetes"
	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/wait"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"github.com/kube-burner/kube-burner/pkg/measurements"
	mmetrics "github.com/kube-burner/kube-burner/pkg/measurements/metrics"
	machinev1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
)


// getWorkerMachineSets lists all machinesets
func getWorkerMachineSets(machineClient *machinev1beta1.MachineV1beta1Client) (map[int][]string) {
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

// waitForNodes waits for all the nodes to be ready
func waitForNodes(clientset kubernetes.Interface, maxWaitTimeout time.Duration) error {
	return wait.PollUntilContextTimeout(context.TODO(), time.Second, maxWaitTimeout, true, func(ctx context.Context) (done bool, err error) {
		nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		for _, node := range nodes.Items {
			if !isNodeReady(&node) {
				log.Debugf("Node %s is not ready\n", node.Name)
				return false, nil
			}
		}
		log.Infof("All nodes are ready")
		return true, nil
	})
}

// isNodeReady checks if a node is ready
func isNodeReady(node *v1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady && condition.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

// getMachineClient creates a reusable machine client
func getMachineClient(restConfig *rest.Config) (*machinev1beta1.MachineV1beta1Client) {
	machineClient, err := machinev1beta1.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("error creating machine API client: %s", err)
	}

	return machineClient
}

// calculateMetrics calculates the metrics for node bootup times
func calculateMetrics(machineSetsToEdit sync.Map, scaledMachineDetails map[string]MachineInfo, nodeMetrics sync.Map, amiID string) ([]interface{}, []interface{}){
	var uuid, machineSetName string
	var normLatencies, latencyQuantiles []interface{}
	for machine, info := range scaledMachineDetails {
		lastHypenIndex := strings.LastIndex(machine, "-")
		if lastHypenIndex != (-1) {
			machineSetName = machine[:lastHypenIndex]
		}
		if _, exists := nodeMetrics.Load(info.nodeUID); !exists {
			continue
		}
		msValue, _ := machineSetsToEdit.Load(machineSetName)
		msInfo := msValue.(MachineSetInfo)
		scaleEventTimestamp := msInfo.lastUpdatedTime
		machineCreationTimeStamp := info.creationTimestamp
		machineReadyTimeStamp := info.readyTimestamp
		nmValue, _ := nodeMetrics.Load(info.nodeUID)
		nodeMetricValue := nmValue.(measurements.NodeMetric)
		uuid = nodeMetricValue.UUID
		normLatencies = append(normLatencies, NodeReadyMetric{
			ScaleEventTimestamp: scaleEventTimestamp,
			MachineCreationTimestamp: machineCreationTimeStamp,
			MachineCreationLatency: int(machineCreationTimeStamp.Sub(scaleEventTimestamp).Milliseconds()),
			MachineReadyTimestamp: machineReadyTimeStamp,
			MachineReadyLatency: int(machineReadyTimeStamp.Sub(scaleEventTimestamp).Milliseconds()),
			NodeCreationTimestamp: nodeMetricValue.Timestamp,
			NodeCreationLatency: int(nodeMetricValue.Timestamp.Sub(scaleEventTimestamp).Milliseconds()),
			NodeReadyTimestamp: nodeMetricValue.NodeReady,
			NodeReadyLatency: int(nodeMetricValue.NodeReady.Sub(scaleEventTimestamp).Milliseconds()),
			MetricName:       nodeReadyLatencyMeasurement,
			UUID:             uuid,
			AMIID: amiID,
			JobName: JobName,
			Name:             nodeMetricValue.Name,
			Labels:           nodeMetricValue.Labels,
		})
	}
	quantileMap := map[string][]float64{}
	for _, normLatency := range normLatencies {
		quantileMap["MachineCreation"] = append(quantileMap["MachineCreation"], float64(normLatency.(NodeReadyMetric).MachineCreationLatency))
		quantileMap["MachineReady"] = append(quantileMap["MachineReady"], float64(normLatency.(NodeReadyMetric).MachineReadyLatency))
		quantileMap["NodeCreation"] = append(quantileMap["NodeCreation"], float64(normLatency.(NodeReadyMetric).NodeCreationLatency))
		quantileMap["NodeReady"] = append(quantileMap["NodeReady"], float64(normLatency.(NodeReadyMetric).NodeReadyLatency))
	}

	calcSummary := func(name string, latencies []float64) mmetrics.LatencyQuantiles {
		latencySummary := mmetrics.NewLatencySummary(latencies, name)
		latencySummary.UUID = uuid
		latencySummary.MetricName = nodeReadyLatencyQuantilesMeasurement
		latencySummary.JobName = JobName
		return latencySummary
	}

	for condition, latencies := range quantileMap {
		latencyQuantiles = append(latencyQuantiles, calcSummary(condition, latencies))
	}
	return normLatencies, latencyQuantiles
}
