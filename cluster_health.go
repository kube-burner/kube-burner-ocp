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

package ocp

import (
	"context"
	"os"

	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/util"
	"github.com/openshift/client-go/config/clientset/versioned"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// cluster health check
func ClusterHealth() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster-health",
		Short: "Checks for ocp cluster health",
		Run: func(cmd *cobra.Command, args []string) {
			ClusterHealthCheck()
		},
	}
	return cmd
}

func ClusterHealthCheck() {
	log.Infof("❤️  Checking for Cluster Health")
	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, restConfig := kubeClientProvider.ClientSet(0, 0)
	openshiftClientset, err := versioned.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("Error creating OpenShift clientset: %v", err)
	}
	if util.ClusterHealthyVanillaK8s(clientSet) && ClusterHealthyOcp(clientSet, openshiftClientset) {
		log.Infof("Cluster is Healthy")
	} else {
		log.Fatalf("Cluster is Unhealthy")
	}
}

func ClusterHealthyOcp(clientset kubernetes.Interface, openshiftClientset *versioned.Clientset) bool {
	var isHealthy = true
	operators, err := openshiftClientset.ConfigV1().ClusterOperators().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Errorf("Error retrieving Cluster Operators: %v", err)
		os.Exit(1)
	}

	for _, operator := range operators.Items {
		// Check availability conditions
		for _, condition := range operator.Status.Conditions {
			if condition.Type == "Available" && condition.Status != "True" { //nolint:goconst
				isHealthy = false
				log.Errorf("Cluster Operator: %s, Condition: %s, Status: %v, Reason: %s", operator.Name, condition.Type, condition.Status, condition.Reason)
			}
		}
	}

	// Rosa osd-cluster-ready check
	job, err := clientset.BatchV1().Jobs("openshift-monitoring").Get(context.TODO(), "osd-cluster-ready", metav1.GetOptions{})
	if err != nil {
		if kerrors.IsNotFound(err) {
			return isHealthy
		}
		log.Errorf("Error getting job/osd-cluster-ready in namespace openshift-monitoring: %v", err)
		isHealthy = false
	} else {
		log.Infof("Checking for status of rosa job/osd-cluster-ready in namespace openshift-monitoring")
		for _, condition := range job.Status.Conditions {
			if condition.Type == "Complete" && condition.Status != "True" { //nolint:goconst
				isHealthy = false
				log.Errorf("job: %s, Condition: %s, Status: %s, Reason: %s", job.Name, condition.Type, condition.Status, condition.Reason)
			}
		}
	}

	return isHealthy
}
