package ocp

import (
	"context"

	"github.com/openshift/client-go/config/clientset/versioned"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

// Verifies container registry and reports its status
func verifyContainerRegistry(restConfig *rest.Config) bool {
	// Create an OpenShift client using the default configuration
	client, err := versioned.NewForConfig(restConfig)
	if err != nil {
		log.Error("Error connecting to the openshift cluster", err)
		return false
	}
	// Get the image registry object
	imageRegistry, err := client.ConfigV1().ClusterOperators().Get(context.TODO(), "image-registry", metav1.GetOptions{})
	if err != nil {
		log.Error("Error getting image registry object:", err)
		return false
	}

	// Check the status conditions
	logMessage := ""
	readyFlag := false
	for _, condition := range imageRegistry.Status.Conditions {
		if condition.Type == "Available" && condition.Status == "True" {
			readyFlag = true
			logMessage += " up and running"
		}
		if condition.Type == "Progressing" && condition.Status == "False" && condition.Reason == "Ready" {
			logMessage += " ready to use"
		}
		if condition.Type == "Degraded" && condition.Status == "False" && condition.Reason == "AsExpected" {
			logMessage += " with a healthy state"
		}
	}
	if readyFlag {
		log.Infof("Cluster image registry is%s", logMessage)
	} else {
		log.Info("Cluster image registry is not up and running")
	}
	return readyFlag
}
