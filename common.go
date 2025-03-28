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
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	k8sconnector "github.com/cloud-bulldozer/go-commons/v2/k8s-connector"
	ocpmetadata "github.com/cloud-bulldozer/go-commons/v2/ocp-metadata"
	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
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

func generateLoopCounterSlice(length int) []string {
	counter := make([]string, length)
	for i := 0; i < length; i++ {
		counter[i] = fmt.Sprint(i + 1)
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

func deletePVsForNamespaces(ctx context.Context, connector k8sconnector.K8SConnector, namespaceNamesMap map[string]struct{}) {
	pvs, err := connector.ClientSet().CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Warnf("Failed listing PVs - %s", err)
		return
	}
	deletingPVs := make(map[string]struct{})
	for _, pv := range pvs.Items {
		// PV not claimed
		if pv.Spec.ClaimRef != nil {
			continue
		}
		// PV not claimed by test namespace
		if _, ok := namespaceNamesMap[pv.Spec.ClaimRef.Namespace]; !ok {
			continue
		}
		// PV will be deleted automatically
		if pv.Spec.PersistentVolumeReclaimPolicy != v1.PersistentVolumeReclaimDelete {
			err = connector.ClientSet().CoreV1().PersistentVolumes().Delete(ctx, pv.Name, metav1.DeleteOptions{})
			if err != nil {
				log.Warnf("Failed to delete PV [%s]: %v", pv.Name, err)
				continue
			}
		}
		deletingPVs[pv.Name] = struct{}{}
	}

	err = wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		pvs, err := connector.ClientSet().CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, pv := range pvs.Items {
			if _, ok := deletingPVs[pv.Name]; ok {
				log.Debugf("Waiting for PV [%s] to be deleted", pv.Name)
				return false, nil
			}
		}
		log.Info("All deleted PVs are deleted")
		return true, nil
	})
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			log.Fatalf("Timeout cleaning up PersistentVolumes: %v", err)
		}
		log.Errorf("Error cleaning up PersistentVolumes: %v", err)
	}
}

func deleteVolumeSnapshotContentForNamespaces(ctx context.Context, connector k8sconnector.K8SConnector, namespaceNamesMap map[string]struct{}) {
	volumeSnapshotContentGVR := schema.GroupVersionResource{Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshotcontents"}
	itemList, err := connector.DynamicClient().Resource(volumeSnapshotContentGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Warnf("Failed listing VolumeSnapshotContents - %s", err)
		return
	}
	deletingVSCs := make(map[string]struct{})
	for _, vsc := range itemList.Items {
		namespace, found, err := unstructured.NestedString(vsc.Object, "spec", "volumeSnapshotRef", "namespace")
		if err != nil {
			log.Warnf("Error reading namespace of volumeSnapshotRef from VolumeSnapshotContent %s: %v", vsc.GetName(), err)
			continue
		}
		if !found {
			log.Warnf("Namespace not found in volumeSnapshotRef of VolumeSnapshotContent %s", vsc.GetName())
			continue
		}
		// VolumeSnapshotContent does not belong to the test namespace
		if _, ok := namespaceNamesMap[namespace]; !ok {
			continue
		}
		deletionPolicy, found, err := unstructured.NestedString(vsc.Object, "spec", "deletionPolicy")
		if err != nil {
			log.Warnf("Error reading deletionPolicy from VolumeSnapshotContent %s: %v", vsc.GetName(), err)
			continue
		}
		if !found {
			log.Warnf("deletionPolicy not found in VolumeSnapshotContent %s", vsc.GetName())
			continue
		}
		// VolumeSnapshotContent will be deleted automatically
		if deletionPolicy != "Delete" {
			err = connector.DynamicClient().Resource(volumeSnapshotContentGVR).Delete(ctx, vsc.GetName(), metav1.DeleteOptions{})
			if err != nil {
				log.Warnf("Failed to delete VolumeSnapshotContent [%s]: %v", vsc.GetName(), err)
				continue
			}
		}
		deletingVSCs[vsc.GetName()] = struct{}{}
	}

	err = wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		itemList, err := connector.DynamicClient().Resource(volumeSnapshotContentGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, vsc := range itemList.Items {
			if _, ok := deletingVSCs[vsc.GetName()]; ok {
				log.Debugf("Waiting for VolumeSnapshotContent [%s] to be deleted", vsc.GetName())
				return false, nil
			}
		}
		log.Info("All deleted VolumeSnapshotContent are deleted")
		return true, nil
	})
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			log.Fatalf("Timeout cleaning up VolumeSnapshotContent: %v", err)
		}
		log.Errorf("Error cleaning up VolumeSnapshotContent: %v", err)
	}
}
