// Copyright 2023 The Kube-burner Authors.
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
	"encoding/json"
	"fmt"
	"os"
	"strings"

	k8sconnector "github.com/cloud-bulldozer/go-commons/v2/k8s-connector"
	k8sstorage "github.com/cloud-bulldozer/go-commons/v2/k8s-storage"
	ocpmetadata "github.com/cloud-bulldozer/go-commons/v2/ocp-metadata"
	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	clusterMetadata ocpmetadata.ClusterMetadata

	accessModeTranslator = map[string]string{
		"RO":  "ReadOnly",
		"RWO": "ReadWriteOnce",
		"RWX": "ReadWriteMany",
	}
)

func setMetrics(cmd *cobra.Command, metricsProfiles []string) {
	profileType, _ := cmd.Root().PersistentFlags().GetString("profile-type")
	switch ProfileType(profileType) {
	case Reporting:
		metricsProfiles = []string{"metrics-report.yml"}
	case Both:
		metricsProfiles = append(metricsProfiles, "metrics-report.yml")
	}
	os.Setenv("METRICS", strings.Join(metricsProfiles, ","))
}

// SetKubeBurnerFlags configures the required environment variables and flags for kube-burner
func GatherMetadata(wh *workloads.WorkloadHelper, alerting bool) error {
	var err error
	kubeClientProvider := config.NewKubeClientProvider("", "")
	_, restConfig := kubeClientProvider.DefaultClientSet()
	wh.MetadataAgent, err = ocpmetadata.NewMetadata(restConfig)
	if err != nil {
		return err
	}
	// When either indexing or alerting are enabled
	if alerting && wh.Config.MetricsEndpoint == "" {
		wh.Config.PrometheusURL, wh.Config.PrometheusToken, err = wh.MetadataAgent.GetPrometheus()
		if err != nil {
			return fmt.Errorf("error obtaining Prometheus information: %v", err)
		}
	}
	clusterMetadata, err = wh.MetadataAgent.GetClusterMetadata()
	if err != nil {
		return err
	}
	jsonData, err := json.Marshal(clusterMetadata)
	if err != nil {
		return err
	}
	json.Unmarshal(jsonData, &wh.SummaryMetadata)
	wh.MetricsMetadata = map[string]interface{}{
		"ocpMajorVersion": clusterMetadata.OCPMajorVersion,
		"ocpVersion":      clusterMetadata.OCPVersion,
	}
	return nil
}

func getK8SConnector() k8sconnector.K8SConnector {
	kubeClientProvider := config.NewKubeClientProvider("", "")
	_, restConfig := kubeClientProvider.DefaultClientSet()
	k8sConnector, err := k8sconnector.NewK8SConnector(restConfig)
	if err != nil {
		log.Fatal(err)
	}
	return k8sConnector
}

func generateLoopCounterSlice(length, startValue int) []string {
	counter := make([]string, length)
	for i := range length {
		counter[i] = fmt.Sprint(i + startValue)
	}
	return counter
}

// Add metadata specific to the CNV workloads
func AddVirtMetadata(wh *workloads.WorkloadHelper, vmImage, udnLayer, udnBindingMethod string) error {
	var err error
	var cnvVersion string
	kubeClientProvider := config.NewKubeClientProvider("", "")
	_, restConfig := kubeClientProvider.DefaultClientSet()
	wh.MetadataAgent, err = ocpmetadata.NewMetadata(restConfig)
	if err != nil {
		return err
	}
	cnvVersion, err = wh.MetadataAgent.GetOCPVirtualizationVersion()
	if err != nil {
		return err
	}
	wh.SummaryMetadata["OCPVirtualizationVersion"] = cnvVersion
	if udnLayer != "" {
		wh.SummaryMetadata["UdnLayer"] = udnLayer
		wh.SummaryMetadata["UdnBindingMethod"] = udnBindingMethod
	}
	wh.SummaryMetadata["VmImage"] = vmImage
	return nil
}

func getStorageAndSnapshotClasses(storageClassNameParam string, useSnapshot, useSnapshotChanged bool) (string, string) {
	k8sConnector := getK8SConnector()

	// Verify provided storage class name or get default of cluster
	storageClassName, err := k8sstorage.GetStorageClassName(k8sConnector, storageClassNameParam, true)
	if err != nil {
		log.Fatal(err)
	}
	if storageClassName == "" {
		if storageClassNameParam == "" {
			log.Fatal("No default StorageClass is set and another was not provided")
		} else {
			log.Fatalf("Provided StorageClass [%v] does not exist", storageClassNameParam)
		}
	}
	log.Infof("Running tests with Storage Class [%s]", storageClassName)

	// If user did not set use-snapshot, get the value from the StorageProfile
	if !useSnapshotChanged {
		sourceFormat, err := k8sstorage.GetDataImportCronSourceFormatForStorageClass(k8sConnector, storageClassName)
		if err != nil {
			log.Fatalf("Failed to get source format for StorageClass [%s] - %v", storageClassName, err)
		}
		useSnapshot = sourceFormat == "snapshot"
		log.Info("The flag use-snapshot was not set. Using the value from the StorageProfile: ", useSnapshot)
	}

	var volumeSnapshotClassName string
	// If using Snapshot, get the VolumeSnapshotClass with the same provisioner as the StorageClass
	if useSnapshot {
		volumeSnapshotClassName, err = k8sstorage.GetVolumeSnapshotClassNameForStorageClass(k8sConnector, storageClassName)
		if err != nil {
			log.Fatalf("Failed to get VolumeSnapshotClass for StorageClass %s - %v", storageClassName, err)
		}
		if volumeSnapshotClassName == "" {
			log.Fatalf("Could not find a corresponding VolumeSnapshotClass for StorageClass %s", storageClassName)
		}
		log.Infof("Running tests with VolumeSnapshotClass [%s]", volumeSnapshotClassName)
	}

	return storageClassName, volumeSnapshotClassName
}
