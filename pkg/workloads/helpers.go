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

package workloads

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"regexp"
	"strings"
	"time"

	k8sconnector "github.com/cloud-bulldozer/go-commons/v2/k8s-connector"
	k8sstorage "github.com/cloud-bulldozer/go-commons/v2/k8s-storage"
	ocpmetadata "github.com/cloud-bulldozer/go-commons/v2/ocp-metadata"
	"github.com/kube-burner/kube-burner/v2/pkg/config"
	kubeburnerutil "github.com/kube-burner/kube-burner/v2/pkg/util"
	"github.com/kube-burner/kube-burner/v2/pkg/util/fileutils"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	kubeBurnerTestNameLabelKey = "kube-burner.io/test-name"
)

var (
	clusterMetadata      ocpmetadata.ClusterMetadata
	clusterCapabilities  ocpmetadata.ClusterCapabilities
	AdditionalVars       map[string]any
	SetVars              map[string]any
	accessModeTranslator = map[string]string{
		"RO":  "ReadOnly",
		"RWO": "ReadWriteOnce",
		"RWX": "ReadWriteMany",
	}
)

func setMetrics(cmd *cobra.Command, metricsProfiles []string) {
	profileType, _ := cmd.Root().PersistentFlags().GetString("profile-type")
	profileTypeFlag := cmd.Root().PersistentFlags().Lookup("profile-type")
	if IsMicroShift() && ProfileType(profileType) == Both && profileTypeFlag != nil && !profileTypeFlag.Changed {
		profileType = string(Regular)
	}
	switch ProfileType(profileType) {
	case Reporting:
		metricsProfiles = []string{"metrics-report.yml"}
	case Both:
		metricsProfiles = append(metricsProfiles, "metrics-report.yml")
	}
	os.Setenv("METRICS", strings.Join(metricsProfiles, ","))
}

// SetKubeBurnerFlags configures the required environment variables and flags for kube-burner
func GatherMetadata(wh *workloads.WorkloadHelper) error {
	var err error
	kubeClientProvider := config.NewKubeClientProvider("", "")
	_, restConfig := kubeClientProvider.DefaultClientSet()
	wh.MetadataAgent, err = ocpmetadata.NewMetadata(restConfig)
	if err != nil {
		return err
	}
	clusterInfo, err := wh.MetadataAgent.GetClusterInfo()
	if err != nil {
		return err
	}
	return applyClusterInfo(wh, clusterInfo)
}

func applyClusterInfo(wh *workloads.WorkloadHelper, clusterInfo ocpmetadata.ClusterInfo) error {
	clusterMetadata = clusterInfo.Metadata
	clusterCapabilities = clusterInfo.Capabilities
	jsonData, err := json.Marshal(clusterMetadata)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(jsonData, &wh.SummaryMetadata); err != nil {
		return err
	}
	wh.MetricsMetadata = metricsMetadataFromClusterMetadata(clusterMetadata)
	return nil
}

func HasAPIGroup(group string) bool {
	return clusterCapabilities.HasAPIGroup(group)
}

func IsMicroShift() bool {
	return clusterMetadata.MicroShift
}

func metricsMetadataFromClusterMetadata(metadata ocpmetadata.ClusterMetadata) map[string]any {
	metricsMetadata := make(map[string]any)
	if metadata.OCPMajorVersion != "" {
		metricsMetadata["ocpMajorVersion"] = metadata.OCPMajorVersion
	}
	if metadata.OCPVersion != "" {
		metricsMetadata["ocpVersion"] = metadata.OCPVersion
	}
	if metadata.Distribution != "" {
		metricsMetadata["distribution"] = metadata.Distribution
	}
	metricsMetadata["microshift"] = metadata.MicroShift
	if metadata.MicroShiftVersion != "" {
		metricsMetadata["microshiftVersion"] = metadata.MicroShiftVersion
	}
	if metadata.MicroShiftMajorVersion != "" {
		metricsMetadata["microshiftMajorVersion"] = metadata.MicroShiftMajorVersion
	}
	if metadata.K8SVersion != "" {
		metricsMetadata["k8sVersion"] = metadata.K8SVersion
	}
	if metadata.TotalNodes != 0 {
		metricsMetadata["totalNodes"] = metadata.TotalNodes
	}
	return metricsMetadata
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

// addWorkloadFlagsToMetadata adds all flag values from the command to SummaryMetadata
func addWorkloadFlagsToMetadata(cmd *cobra.Command, wh *workloads.WorkloadHelper) {
	workloadFlags := make(map[string]string)
	// Use LocalFlags() instead of Flags() to only get flags specific to this command
	cmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Name == "help" {
			return
		}
		flagName := kebabToCamelCase(flag.Name)
		workloadFlags[flagName] = flag.Value.String()
	})
	wh.SummaryMetadata["workloadFlags"] = workloadFlags
}

// RunWorkload executes the common workload pattern: adds flags to metadata, sets variables, and runs the workload
func RunWorkload(cmd *cobra.Command, wh *workloads.WorkloadHelper, configFile string) int {
	defaultUndefinedTemplateVars(configFile)
	addWorkloadFlagsToMetadata(cmd, wh)
	wh.SetVariables(AdditionalVars, SetVars)
	return wh.Run(configFile)
}

var (
	templateBlockRegex = regexp.MustCompile(`\{\{.*?\}\}`)
	templateVarRegex   = regexp.MustCompile(`\.([a-zA-Z_][a-zA-Z0-9_]*)`)
	numericCmpRegex    = regexp.MustCompile(`(?:ne|eq|gt|lt|ge|le)\s+\.([a-zA-Z_][a-zA-Z0-9_]*)\s+\d`)
)

// defaultUndefinedTemplateVars reads the config file and sets empty defaults for any
// template variables that are not already defined in AdditionalVars, SetVars, or
// the environment. This prevents template rendering failures when running configs
// (extracted or custom) that reference variables not explicitly set by the current
// workload command.
func defaultUndefinedTemplateVars(configFile string) {
	if AdditionalVars == nil {
		AdditionalVars = make(map[string]any)
	}
	reader, err := fileutils.GetWorkloadReader(configFile, nil)
	if err != nil {
		log.Debugf("Failed to open config %s for template variable discovery: %v", configFile, err)
		return
	}
	defer reader.Close()
	configContent, err := io.ReadAll(reader)
	if err != nil {
		log.Debugf("Failed to read config %s for template variable discovery: %v", configFile, err)
		return
	}

	content := string(configContent)

	numericVars := make(map[string]bool)
	for _, match := range numericCmpRegex.FindAllStringSubmatch(content, -1) {
		numericVars[match[1]] = true
	}

	seen := make(map[string]bool)
	var undefined []string
	for _, block := range templateBlockRegex.FindAllString(content, -1) {
		for _, match := range templateVarRegex.FindAllStringSubmatch(block, -1) {
			varName := match[1]
			if seen[varName] {
				continue
			}
			seen[varName] = true
			if _, exists := AdditionalVars[varName]; exists {
				continue
			}
			if v, exists := SetVars[varName]; exists {
				AdditionalVars[varName] = v
				delete(SetVars, varName)
				continue
			}
			if _, envSet := os.LookupEnv(varName); envSet {
				continue
			}
			if numericVars[varName] {
				AdditionalVars[varName] = 0
			} else {
				AdditionalVars[varName] = ""
			}
			undefined = append(undefined, varName)
		}
	}
	if len(undefined) > 0 {
		log.Warningf("Template variables [%s] are not defined and will default to empty/zero values. "+
			"Set them as environment variables (e.g., %s=<value> kube-burner-ocp ...) or use --set %s=<value>",
			strings.Join(undefined, ", "), undefined[0], undefined[0])
	}
}

func setIfFlagChanged(cmd *cobra.Command, flagValues map[string]any) {
	for flag, value := range flagValues {
		if cmd.Flags().Changed(flag) {
			AdditionalVars[strings.ToUpper(strings.ReplaceAll(flag, "-", "_"))] = value
		}
	}
}

// kebabToCamelCase converts a flag name from kebab-case to camelCase
func kebabToCamelCase(word string) string {
	parts := strings.Split(word, "-")
	for i := range parts {
		// First part stays lowercase, rest are capitalized
		if i == 0 {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
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

func deletePVsForNamespaces(ctx context.Context, connector k8sconnector.K8SConnector, namespaceNamesMap map[string]struct{}) {
	pvs, err := connector.ClientSet().CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		log.Warnf("Failed listing PVs - %s", err)
		return
	}
	deletingPVs := make(map[string]struct{})
	for _, pv := range pvs.Items {
		// PV not claimed
		if pv.Spec.ClaimRef == nil {
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

func cleanupTestNamespaces(ctx context.Context, labelSelector string) {
	k8sConnector := getK8SConnector()
	ns, err := k8sConnector.ClientSet().CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		log.Fatalf("Error listing namespaces: %v", err.Error())
	}

	if len(ns.Items) == 0 {
		log.Info("No Namespaces were found for previous test")
		return
	}

	kubeburnerutil.CleanupNamespacesByLabel(ctx, k8sConnector.ClientSet(), labelSelector)

	namespaceNamesMap := make(map[string]struct{}, len(ns.Items))
	for _, ns := range ns.Items {
		namespaceNamesMap[ns.Name] = struct{}{}
	}

	deleteVolumeSnapshotContentForNamespaces(ctx, k8sConnector, namespaceNamesMap)
	deletePVsForNamespaces(ctx, k8sConnector, namespaceNamesMap)

}

func verifyOrGetRandomWorkerNodeName(workerNodeName string) string {
	k8sConnector := getK8SConnector()

	nodes, err := k8sConnector.ClientSet().CoreV1().Nodes().List(context.Background(), metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/worker"})
	if err != nil {
		log.Fatalf("Error getting nodes: %v", err)
		return ""
	}

	workerNodeNamesMap := make(map[string]struct{}, len(nodes.Items))
	for _, node := range nodes.Items {
		workerNodeNamesMap[node.Name] = struct{}{}
	}

	if workerNodeName != "" {
		if _, ok := workerNodeNamesMap[workerNodeName]; !ok {
			log.Fatalf("Provided worker node %s does not exist", workerNodeName)
		}
		return workerNodeName
	}

	workerNodeNamesArray := make([]string, 0, len(workerNodeNamesMap))
	for k := range workerNodeNamesMap {
		workerNodeNamesArray = append(workerNodeNamesArray, k)
	}

	return workerNodeNamesArray[rand.Intn(len(workerNodeNamesArray))]
}
