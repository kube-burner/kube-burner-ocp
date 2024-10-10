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
	"strings"
	"time"

	machinev1beta1 "github.com/openshift/client-go/machine/clientset/versioned/typed/machine/v1beta1"
	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	infrav1 "sigs.k8s.io/cluster-api-provider-aws/v2/api/v1beta2"
	"sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// helper function to create a pointer to an int32
func int32Ptr(i int32) *int32 {
	return &i
}

// discardPreviousMachines updates the current machines details discarding the previous ones
func discardPreviousMachines(prevMachineDetails map[string]MachineInfo, currentMachineDetails map[string]MachineInfo) {
	for key := range currentMachineDetails {
		if _, exists := prevMachineDetails[key]; exists {
			delete(currentMachineDetails, key)
		}
	}
}

// getMachineClient creates a reusable machine client
func getMachineClient(restConfig *rest.Config) *machinev1beta1.MachineV1beta1Client {
	machineClient, err := machinev1beta1.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("error creating machine API client: %s", err)
	}

	return machineClient
}

// getCAPIClient create a cluster api client
func getCAPIClient(restConfig *rest.Config) client.Client {
	capiScheme := runtime.NewScheme()
	if err := v1beta1.AddToScheme(capiScheme); err != nil {
		log.Fatalf("error adding CAPI types to scheme: %s", err)
	}
	if err := infrav1.AddToScheme(capiScheme); err != nil {
		log.Fatalf("error adding AWS CAPI types to scheme: %s", err)
	}
	capiClient, err := client.New(restConfig, client.Options{Scheme: capiScheme})
	if err != nil {
		log.Fatalf("error creating CAPI client: %s", err)
	}

	return capiClient
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

// waitForNodes waits for all the nodes to be ready
func waitForNodes(clientset kubernetes.Interface) error {
	return wait.PollUntilContextTimeout(context.TODO(), time.Second, maxWaitTimeout, true, func(ctx context.Context) (done bool, err error) {
		nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}

		for _, node := range nodes.Items {
			if !isNodeReady(&node) {
				log.Debugf("Node %s is not ready", node.Name)
				return false, nil
			}
		}
		log.Infof("All nodes are ready")
		return true, nil
	})
}

// getHCNamespace gets the longest hosted cluster namespace from management cluster
func getHCNamespace(clientset kubernetes.Interface, clusterID string) string {
	namespaces, err := clientset.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		log.Fatalf("Error listing the namespaces: %s", err)
	}

	longestNamespace := ""
	maxLength := 0
	for _, ns := range namespaces.Items {
		if strings.Contains(ns.Name, clusterID) {
			if len(ns.Name) > maxLength {
				longestNamespace = ns.Name
				maxLength = len(ns.Name)
			}
		}
	}
	return longestNamespace
}
