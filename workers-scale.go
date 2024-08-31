// Copyright 2022 The Kube-burner Authors.
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

package ocp

import (
	"os"
	"sort"
	"strings"
	"encoding/json"
	"time"
	"context"
	"fmt"
	"sync"

	"github.com/cloud-bulldozer/go-commons/indexers"
	ocpmetadata "github.com/cloud-bulldozer/go-commons/ocp-metadata"
	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/measurements"
	mtypes "github.com/kube-burner/kube-burner/pkg/measurements/types"
	"github.com/kube-burner/kube-burner/pkg/util/metrics"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/rest"
	"k8s.io/apimachinery/pkg/util/wait"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	machinev1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"

)


type AWSProviderSpec struct {
	AMI struct {
		ID string `json:"id"`
	} `json:"ami"`
}

type MachineInfo struct {
	nodeUID string
	creationTimestamp time.Time
}

type MachineSetInfo struct {
	lastUpdatedTime time.Time
	prevReplicas int
	currentReplicas int
}

// NewWorkersScale orchestrates scaling workers in ocp wrapper
func NewWorkersScale(metricsEndpoint *string, ocpMetaAgent *ocpmetadata.Metadata) *cobra.Command {
	var metricsProfile, jobName string
	var userMetadata, metricsDirectory string
	var prometheusStep time.Duration
	var uuid string
	var err error
	var rc, additionalWorkerNodes int
	var prometheusURL, prometheusToken string
	var tarballName string
	var indexer config.MetricsEndpoint
	var clusterMetadataMap map[string]interface{}
	cmd := &cobra.Command{
		Use:          "workers-scale",
		Short:        "Runs workers-scale sub-command",
		Long:         "If no other indexer is specified, local indexer is used by default",
		SilenceUsage: true,
		PostRun: func(cmd *cobra.Command, args []string) {
			log.Info("👋 Exiting kube-burner ", uuid)
			os.Exit(rc)
		},
		Run: func(cmd *cobra.Command, args []string) {
			uuid, _ = cmd.Flags().GetString("uuid")
			esServer, _ := cmd.Flags().GetString("es-server")
			esIndex, _ := cmd.Flags().GetString("es-index")
			workloads.ConfigSpec.GlobalConfig.UUID = uuid
			// When metricsEndpoint is specified, don't fetch any prometheus token
			if *metricsEndpoint == "" {
				prometheusURL, prometheusToken, err = ocpMetaAgent.GetPrometheus()
				if err != nil {
					log.Fatal("Error obtaining prometheus information from cluster: ", err.Error())
				}
			}
			metricsProfiles := strings.FieldsFunc(metricsProfile, func(r rune) bool {
				return r == ',' || r == ' '
			})
			indexer = config.MetricsEndpoint{
				Endpoint:      prometheusURL,
				Token:         prometheusToken,
				Step:          prometheusStep,
				Metrics:       metricsProfiles,
				SkipTLSVerify: true,
			}
			if esServer != "" && esIndex != "" {
				indexer.IndexerConfig = indexers.IndexerConfig{
					Type:    indexers.ElasticIndexer,
					Servers: []string{esServer},
					Index:   esIndex,
				}
			} else {
				if metricsDirectory == "collected-metrics" {
					metricsDirectory = metricsDirectory + "-" + uuid
				}
				indexer.IndexerConfig = indexers.IndexerConfig{
					Type:             indexers.LocalIndexer,
					MetricsDirectory: metricsDirectory,
					TarballName:      tarballName,
				}
			}

			metadata := make(map[string]interface{})
			jsonData, _ := json.Marshal(clusterMetadata)
			json.Unmarshal(jsonData, &clusterMetadataMap)
			for k, v := range clusterMetadataMap {
				metadata[k] = v
			}
			workloads.ConfigSpec.MetricsEndpoints = append(workloads.ConfigSpec.MetricsEndpoints, indexer)
			metricsScraper := metrics.ProcessMetricsScraperConfig(metrics.ScraperConfig{
				ConfigSpec:      &workloads.ConfigSpec,
				MetricsEndpoint: *metricsEndpoint,
				UserMetaData:    userMetadata,
				RawMetadata:     metadata,
			})
			clusterMetadata, err = ocpMetaAgent.GetClusterMetadata()
			if err != nil {
				log.Fatal("Error obtaining clusterMetadata: ", err.Error())
			}
			kubeClientProvider := config.NewKubeClientProvider("", "")
			clientSet, restConfig := kubeClientProvider.ClientSet(0, 0)
			machineClient := getMachineClient(restConfig)
			const namespace = "openshift-machine-api"
			MachineSetDetails := getWorkerMachineSets(machineClient, namespace)
			prevMachineDetails, amiID := getMachines(machineClient, namespace)
			log.Infof("%v %v", prevMachineDetails, amiID)
			configSpec := config.Spec{
				GlobalConfig: config.GlobalConfig {
					Measurements: []mtypes.Measurement {
						{
							Name: "nodeLatency",
						},
					},
				},
			}
			measurements.NewMeasurementFactory(configSpec, metricsScraper.Metadata)
			measurements.SetJobConfig(
				&config.Job{
					Name:                 jobName,
				},
				kubeClientProvider,
			)
			measurements.Start()
			machineSetsToEdit := scaleMachineSets(machineClient, namespace, MachineSetDetails, additionalWorkerNodes)
			if err = waitForNodes(clientSet, 4*time.Hour); err != nil {
				log.Infof("Error waiting for nodes: %v", err)
			} else {
				log.Infof("All nodes are ready")
			}
			if err = measurements.Stop(); err != nil {
				log.Error(err.Error())
			}
			nodeMetrics := measurements.GetMetrics()
			log.Infof("%v", nodeMetrics)
			log.Infof("Restoring machine sets to previous state")
			editMachineSets(machineClient, namespace, machineSetsToEdit, false)
		},
	}
	cmd.Flags().StringVarP(&metricsProfile, "metrics-profile", "m", "metrics.yml", "comma-separated list of metric profiles")
	cmd.Flags().StringVar(&metricsDirectory, "metrics-directory", "collected-metrics", "Directory to dump the metrics files in, when using default local indexing")
	cmd.Flags().DurationVar(&prometheusStep, "step", 30*time.Second, "Prometheus step size")
	cmd.Flags().IntVar(&additionalWorkerNodes, "additional-worker-nodes", 3, "additional workers to scale")
	cmd.Flags().StringVar(&jobName, "job-name", "workers-scale", "Indexing job name")
	cmd.Flags().StringVar(&userMetadata, "user-metadata", "", "User provided metadata file, in YAML format")
	cmd.Flags().StringVar(&tarballName, "tarball-name", "", "Dump collected metrics into a tarball with the given name, requires local indexing")
	cmd.Flags().SortFlags = false
	return cmd
}

// waitForNodes waits for all the nodes to be ready
func waitForNodes(clientset kubernetes.Interface, timeout time.Duration) error {
	return wait.PollImmediate(10*time.Second, timeout, func() (bool, error) {
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

// scaleMachineSets triggers scale operation on the machinesets
func scaleMachineSets(machineClient *machinev1beta1.MachineV1beta1Client, namespace string, machineSetReplicas map[int][]string, desiredWorkerCount int) (map[string]MachineSetInfo){
	var lastIndex int
	machineSetsToEdit := make(map[string]MachineSetInfo)
	keys := make([]int, 0, len(machineSetReplicas))
	for key := range machineSetReplicas {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	for _, value := range keys {
		if machineSets, exists := machineSetReplicas[value]; exists {
			for index, machineSet := range machineSets {
				if desiredWorkerCount > 0 {
					if _, exists := machineSetsToEdit[machineSet]; !exists {
						machineSetsToEdit[machineSet] = MachineSetInfo{
							prevReplicas: value,
							currentReplicas: value + 1,
						}
					}
					msInfo := machineSetsToEdit[machineSet]
					msInfo.currentReplicas = value + 1
					machineSetsToEdit[machineSet] = msInfo
					machineSetReplicas[value + 1] = append(machineSetReplicas[value + 1], machineSet)
					lastIndex = index
					desiredWorkerCount--
				} else {
					break
				}
			}
			if lastIndex == len(machineSets) - 1 {
				delete(machineSetReplicas, value)
			} else {
				machineSetReplicas[value] = machineSets[lastIndex + 1:]
			}
		} else if desiredWorkerCount > 0 {
			break
		}
	}
	editMachineSets(machineClient, namespace, machineSetsToEdit, true)
	return machineSetsToEdit
}

// editMachineSets edits machinesets parallely
func editMachineSets(machineClient *machinev1beta1.MachineV1beta1Client, namespace string, machineSetsToEdit map[string]MachineSetInfo, isScaleUp bool) {
	var wg sync.WaitGroup
	maxWaitTimeout := 4 * time.Hour
	for machineSet, msInfo := range machineSetsToEdit {
		var replica int
		if isScaleUp {
			replica = msInfo.currentReplicas
		} else {
			replica = msInfo.prevReplicas
		}
		wg.Add(1)
		go func(ms string, r int) {
			defer wg.Done()
			err := updateMachineSetReplicas(machineClient, ms, namespace, int32(r), maxWaitTimeout, machineSetsToEdit)
            if err != nil {
                log.Errorf("Failed to edit MachineSet %s: %v", ms, err)
            }
		}(machineSet, replica)
	}
	wg.Wait()
	log.Infof("All the machinesets have been editted")
}

// updateMachineSetsReplicas updates machines replicas
func updateMachineSetReplicas(machineClient *machinev1beta1.MachineV1beta1Client, name string, namespace string, newReplicaCount int32, maxWaitTimeout time.Duration, machineSetsToEdit map[string]MachineSetInfo) error {
    machineSet, err := machineClient.MachineSets(namespace).Get(context.TODO(), name, metav1.GetOptions{})
    if err != nil {
        return fmt.Errorf("error getting machineset: %s", err)
    }

    machineSet.Spec.Replicas = &newReplicaCount
	updateTimestamp := time.Now().UTC()
    _, err = machineClient.MachineSets(namespace).Update(context.TODO(), machineSet, metav1.UpdateOptions{})
    if err != nil {
        return fmt.Errorf("error updating machineset: %s", err)
    }
	msInfo := machineSetsToEdit[name]
	msInfo.lastUpdatedTime = updateTimestamp
	machineSetsToEdit[name] = msInfo

	err = waitForMachineSet(machineClient, name, namespace, newReplicaCount, maxWaitTimeout)
	if err != nil {
        return fmt.Errorf("timeout waiting for MachineSet %s to be ready: %v", name, err)
    }

    log.Infof("MachineSet %s updated to %d replicas", name, newReplicaCount)
	return nil
}

// waitForMachineSet waits for machinesets to be ready with new replica count
func waitForMachineSet(machineClient *machinev1beta1.MachineV1beta1Client, name, namespace string, newReplicaCount int32, maxWaitTimeout time.Duration) error {
	return wait.PollUntilContextTimeout(context.TODO(), time.Second, maxWaitTimeout, true, func(ctx context.Context) (done bool, err error) {
        ms, err := machineClient.MachineSets(namespace).Get(context.TODO(), name, metav1.GetOptions{})
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

// getMachineClient creates a reusable machine client
func getMachineClient(restConfig *rest.Config) (*machinev1beta1.MachineV1beta1Client) {
	machineClient, err := machinev1beta1.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("error creating machine API client: %s", err)
	}

	return machineClient
}

// getWorkerMachineSets lists all machinesets
func getWorkerMachineSets(machineClient *machinev1beta1.MachineV1beta1Client, namespace string) (map[int][]string) {
	machineSetReplicas := make(map[int][]string)
	machineSets, err := machineClient.MachineSets(namespace).List(context.TODO(), metav1.ListOptions{})
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
func getMachines(machineClient *machinev1beta1.MachineV1beta1Client, namespace string) (map[string]MachineInfo, string) {
	var amiID string
	machineDetails := make(map[string]MachineInfo)
	machines, err := machineClient.Machines(namespace).List(context.TODO(), metav1.ListOptions{})
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
				machineDetails[machine.Name] = MachineInfo{
					nodeUID: string(machine.Status.NodeRef.UID),
					creationTimestamp: machine.CreationTimestamp.Time.UTC(),
				}
			}
		}
	}
	log.Debugf("Machines: %v with amiID: %v", machineDetails, amiID)
	return machineDetails, amiID
}
