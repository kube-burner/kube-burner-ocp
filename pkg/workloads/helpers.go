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
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloud-bulldozer/go-commons/v2/indexers"
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
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	kubeBurnerTestNameLabelKey = "kube-burner.io/test-name"
	// WorkerNodeSelector is the default node selector for workloads targeting worker nodes
	WorkerNodeSelector = "node-role.kubernetes.io/worker=,node-role.kubernetes.io/infra!=,node-role.kubernetes.io/workload!="
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

// buildNodeSelectorJSON converts a Kubernetes label selector string to a NodeSelector JSON string
// that can be used in pod affinity specifications. The selector string uses the standard Kubernetes
// label selector format (e.g., "node-role.kubernetes.io/worker=,node-role.kubernetes.io/infra!=").
func buildNodeSelectorJSON(selector string) (string, error) {
	var nodeSelector v1.NodeSelector
	var matchExpressions []v1.NodeSelectorRequirement

	labelSelector, err := labels.Parse(selector)
	if err != nil {
		return "", err
	}

	reqList, _ := labelSelector.Requirements()
	for _, req := range reqList {
		matchExpression := v1.NodeSelectorRequirement{
			Key: req.Key(),
		}
		// Even with a nil value, the list is not empty, so we need to check its value
		if req.Values().List()[0] == "" {
			if req.Operator() == "=" {
				matchExpression.Operator = v1.NodeSelectorOpExists
			} else if req.Operator() == "!=" {
				matchExpression.Operator = v1.NodeSelectorOpDoesNotExist
			}
		} else {
			matchExpression.Operator = v1.NodeSelectorOpIn
			matchExpression.Values = req.Values().List()
		}
		matchExpressions = append(matchExpressions, matchExpression)
	}

	nodeSelector.NodeSelectorTerms = []v1.NodeSelectorTerm{{MatchExpressions: matchExpressions}}
	nodeSelectorJSON, err := json.Marshal(nodeSelector)
	if err != nil {
		return "", err
	}

	return string(nodeSelectorJSON), nil
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
func startAndMeasureVMs(ctx context.Context, bulkSleepTime time.Duration, sshConcurrencyLimit int) []bootstormResult {
	k8sConnector := getK8SConnector()
	vmGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}

	vmList, err := k8sConnector.DynamicClient().Resource(vmGVR).Namespace(bootstormNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("kube-burner.io/job=%s", bootstormJobLabel),
	})
	if err != nil {
		log.Errorf("Failed to list VMs: %v", err)
		return nil
	}

	vmNames := make([]string, 0, len(vmList.Items))
	for _, vm := range vmList.Items {
		vmNames = append(vmNames, vm.GetName())
	}
	sort.Slice(vmNames, func(i, j int) bool {
		getIter := func(name string) int {
			parts := strings.Split(name, "-")
			if len(parts) >= 3 {
				n, _ := strconv.Atoi(parts[len(parts)-2])
				return n
			}
			return 0
		}
		return getIter(vmNames[i]) < getIter(vmNames[j])
	})
	if len(vmNames) == 0 {
		log.Errorf("No VMs found in namespace %s", bootstormNamespace)
		return nil
	}

	log.Infof("Waiting for %d DVs to reach Succeeded...", len(vmNames))
	failedDVs := make(map[string]bool)
	var dvMu sync.Mutex
	var dvWg sync.WaitGroup
	for _, name := range vmNames {
		dvWg.Add(1)
		go func(vmName string) {
			defer dvWg.Done()
			dvName := strings.Replace(vmName, "windows-bootstorm-", "windows-bootstorm-rootdisk-", 1)
			for attempt := 0; attempt < dvMaxRetries; attempt++ {
				if ctx.Err() != nil {
					return
				}
				output, err := exec.CommandContext(ctx, "kubectl", "get", "dv", dvName, "-n", bootstormNamespace,
					"-o", "jsonpath={.status.phase}").Output()
				if err != nil {
					time.Sleep(sshPollInterval)
					continue
				}
				phase := strings.TrimSpace(string(output))
				if phase == "Succeeded" {
					return
				}
				if phase == "Failed" {
					log.Warnf("DV %s failed", dvName)
					dvMu.Lock()
					failedDVs[vmName] = true
					dvMu.Unlock()
					return
				}
				time.Sleep(sshPollInterval)
			}
			log.Warnf("DV %s not ready after max retries", dvName)
			dvMu.Lock()
			failedDVs[vmName] = true
			dvMu.Unlock()
		}(name)
	}
	dvWg.Wait()
	if len(failedDVs) > 0 {
		log.Warnf("%d DVs failed or timed out, skipping those VMs", len(failedDVs))
	}
	readyVMs := make([]string, 0, len(vmNames))
	for _, name := range vmNames {
		if !failedDVs[name] {
			readyVMs = append(readyVMs, name)
		}
	}
	log.Infof("DV polling finished. Starting %d VMs (concurrency: %d)", len(readyVMs), sshConcurrencyLimit)

	var allResults []bootstormResult
	var mu sync.Mutex
	firstStartTime := time.Now()

	for i := 0; i < len(readyVMs); i += sshConcurrencyLimit {
		end := i + sshConcurrencyLimit
		if end > len(readyVMs) {
			end = len(readyVMs)
		}
		bulk := readyVMs[i:end]
		log.Infof("Starting bulk %d-%d (%d VMs)", i, end-1, len(bulk))

		var wg sync.WaitGroup
		for _, vmName := range bulk {
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				startTime := time.Now()
				if err := exec.CommandContext(ctx, "virtctl", "start", name, "-n", bootstormNamespace).Run(); err != nil {
					log.Warnf("Failed to start VM %s: %v", name, err)
					mu.Lock()
					allResults = append(allResults, bootstormResult{VMName: name, AccessVM: 0})
					mu.Unlock()
					return
				}
				result := waitForSSH(ctx, name, startTime)
				result.TotalRunTime = float64(time.Since(firstStartTime).Milliseconds())
				mu.Lock()
				allResults = append(allResults, result)
				mu.Unlock()
			}(vmName)
		}
		wg.Wait()

		if end < len(readyVMs) {
			log.Infof("Bulk complete, resting %s", bulkSleepTime)
			select {
			case <-ctx.Done():
				return allResults
			case <-time.After(bulkSleepTime):
			}
		}
	}

	log.Infof("Boot measurement complete: %d/%d VMs", len(allResults), len(readyVMs))
	return allResults
}

func waitForSSH(ctx context.Context, vmName string, startTime time.Time) bootstormResult {
	for attempt := 0; attempt < sshMaxRetries; attempt++ {
		if ctx.Err() != nil {
			return bootstormResult{VMName: vmName, AccessVM: 0}
		}
		output, err := exec.CommandContext(ctx, "kubectl", "get", "vm", vmName, "-n", bootstormNamespace,
			"-o", "jsonpath={.status.printableStatus}").Output()
		if err != nil {
			log.Warnf("Failed to get VM %s status: %v", vmName, err)
			time.Sleep(sshPollInterval)
			continue
		}
		if strings.TrimSpace(string(output)) == "Running" {
			break
		}
		if attempt == sshMaxRetries-1 {
			log.Warnf("VM %s not Running after max retries", vmName)
			return bootstormResult{VMName: vmName, AccessVM: 0}
		}
		time.Sleep(sshPollInterval)
	}

	for attempt := 1; attempt <= sshMaxRetries; attempt++ {
		if ctx.Err() != nil {
			break
		}

		output, _ := exec.CommandContext(ctx, "virtctl", "ssh",
			"--local-ssh-opts=-o BatchMode=yes",
			"--local-ssh-opts=-o PasswordAuthentication=no",
			"--local-ssh-opts=-o ConnectTimeout=2",
			"--local-ssh-opts=-o StrictHostKeyChecking=no",
			"--local-ssh-opts=-o UserKnownHostsFile=/dev/null",
			"-n", bootstormNamespace,
			"-c", "exit",
			"--username", "root",
			fmt.Sprintf("vm/%s", vmName),
		).CombinedOutput()

		outStr := strings.ToLower(string(output))
		if strings.Contains(outStr, "permission denied") || strings.Contains(outStr, "verification failed") {
			elapsed := float64(time.Since(startTime).Microseconds()) / 1000.0
			node := getVMINode(ctx, vmName)
			log.Infof("SSH accessible: %s on %s in %.1fms", vmName, node, elapsed)
			return bootstormResult{
				VMName:        vmName,
				Node:          node,
				BootstormTime: float64(elapsed),
				AccessVM:      1,
			}
		}

		time.Sleep(sshPollInterval)
	}

	log.Warnf("SSH not accessible for %s after max retries", vmName)
	node := getVMINode(ctx, vmName)
	return bootstormResult{VMName: vmName, Node: node, AccessVM: 0}
}

func getVMINode(ctx context.Context, vmName string) string {
	output, err := exec.CommandContext(ctx, "kubectl", "get", "vmi", vmName, "-n", bootstormNamespace,
		"-o", "jsonpath={.status.nodeName}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func writeBootstormResults(results []bootstormResult, wh *workloads.WorkloadHelper, imageURL string, scale int) {
	if len(results) == 0 {
		log.Warn("No bootstorm SSH results to index")
		return
	}

	vmOSVersion := strings.TrimSuffix(path.Base(imageURL), path.Ext(imageURL))

	docs := make([]interface{}, len(results))
	for i, r := range results {
		runStatus := "complete"
		if r.AccessVM == 0 {
			runStatus = "failed"
		}
		docs[i] = map[string]any{
			"vm_name":        r.VMName,
			"node":           r.Node,
			"bootstorm_time": r.BootstormTime,
			"access_vm":      r.AccessVM,
			"total_run_time": r.TotalRunTime,
			"kind":           "vm",
			"run_status":     runStatus,
			"vm_os_version":  vmOSVersion,
			"scale":          scale,
			"uuid":           AdditionalVars["UUID"],
			"metricName":     "bootstormSSHMeasurement",
			"jobName":        bootstormJobLabel,
			"timestamp":      time.Now().UTC().Format(time.RFC3339),
			"metadata":       wh.MetricsMetadata,
		}
	}

	for _, endpoint := range workloads.ConfigSpec.MetricsEndpoints {
		if endpoint.Type == "" {
			continue
		}
		idx, err := indexers.NewIndexer(endpoint.IndexerConfig)
		if err != nil {
			log.Errorf("Failed to create indexer: %v", err)
			continue
		}
		resp, err := (*idx).Index(docs, indexers.IndexingOpts{
			MetricName: "bootstormSSHMeasurement",
		})
		if err != nil {
			log.Errorf("Failed to index bootstorm SSH results: %v", err)
		} else {
			log.Infof("Bootstorm SSH results: %s", resp)
		}
	}
}

func stopAndDeleteBootstormVMs(ctx context.Context) {
	k8sConnector := getK8SConnector()
	vmGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	vmiGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances"}

	vmList, err := k8sConnector.DynamicClient().Resource(vmGVR).Namespace(bootstormNamespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(vmList.Items) == 0 {
		return
	}

	log.Infof("Stopping %d VMs", len(vmList.Items))
	for _, vm := range vmList.Items {
		_ = exec.CommandContext(ctx, "virtctl", "stop", vm.GetName(), "-n", bootstormNamespace).Run()
	}

	deadline := time.After(5 * time.Minute)
	for {
		select {
		case <-deadline:
			log.Warnf("Timed out waiting for VMIs to terminate")
			goto deleteVMs
		case <-time.After(10 * time.Second):
			vmiList, err := k8sConnector.DynamicClient().Resource(vmiGVR).Namespace(bootstormNamespace).List(ctx, metav1.ListOptions{})
			if err != nil || len(vmiList.Items) == 0 {
				log.Infof("All VMIs terminated")
				goto deleteVMs
			}
			log.Infof("Waiting for %d VMIs to terminate...", len(vmiList.Items))
		}
	}

deleteVMs:
	log.Infof("Deleting %d VMs", len(vmList.Items))
	for _, vm := range vmList.Items {
		_ = k8sConnector.DynamicClient().Resource(vmGVR).Namespace(bootstormNamespace).Delete(ctx, vm.GetName(), metav1.DeleteOptions{})
	}

	vmDeadline := time.After(5 * time.Minute)
	for {
		select {
		case <-vmDeadline:
			log.Warnf("Timed out waiting for VMs to be deleted")
			return
		case <-time.After(10 * time.Second):
			remaining, err := k8sConnector.DynamicClient().Resource(vmGVR).Namespace(bootstormNamespace).List(ctx, metav1.ListOptions{})
			if err != nil || len(remaining.Items) == 0 {
				log.Infof("All VMs deleted")
				return
			}
			log.Infof("Waiting for %d VMs to be deleted...", len(remaining.Items))
		}
	}
}

func deleteBootstormDVs(ctx context.Context) {
	k8sConnector := getK8SConnector()
	dvGVR := schema.GroupVersionResource{Group: "cdi.kubevirt.io", Version: "v1beta1", Resource: "datavolumes"}

	dvList, err := k8sConnector.DynamicClient().Resource(dvGVR).Namespace(bootstormNamespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(dvList.Items) == 0 {
		return
	}

	log.Infof("Deleting %d DataVolumes", len(dvList.Items))
	for _, dv := range dvList.Items {
		_ = k8sConnector.DynamicClient().Resource(dvGVR).Namespace(bootstormNamespace).Delete(ctx, dv.GetName(), metav1.DeleteOptions{})
	}

	deadline := time.After(5 * time.Minute)
	for {
		select {
		case <-deadline:
			log.Warnf("Timed out waiting for DVs to be deleted")
			return
		case <-time.After(10 * time.Second):
			remaining, err := k8sConnector.DynamicClient().Resource(dvGVR).Namespace(bootstormNamespace).List(ctx, metav1.ListOptions{})
			if err != nil || len(remaining.Items) == 0 {
				log.Infof("All DataVolumes deleted")
				return
			}
			log.Infof("Waiting for %d DVs to be deleted...", len(remaining.Items))
		}
	}
}

func clearWorkerNodeCaches() {
	k8s := getK8SConnector()
	nodes, err := k8s.ClientSet().CoreV1().Nodes().List(context.Background(), metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/worker"})
	if err != nil {
		log.Warnf("Failed to list worker nodes for cache clear: %v", err)
		return
	}
	for i := 0; i < 3; i++ {
		for _, node := range nodes.Items {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			_ = exec.CommandContext(ctx, "oc", "debug", "node/"+node.Name, "--", "chroot", "/host", "sh", "-c", "sync; echo 3 > /proc/sys/vm/drop_caches").Run()
			cancel()
		}
		if i < 2 {
			time.Sleep(3 * time.Second)
		}
	}
	log.Infof("Cleared buffer caches on %d worker nodes", len(nodes.Items))
}

func createSnapshotCloneSC(ctx context.Context, baseSCName string) (string, error) {
	k8s := getK8SConnector()
	newName := baseSCName + "-bootstorm"

	if _, err := k8s.ClientSet().StorageV1().StorageClasses().Get(ctx, newName, metav1.GetOptions{}); err == nil {
		log.Infof("StorageClass %s already exists, reusing", newName)
		return newName, nil
	}

	baseSC, err := k8s.ClientSet().StorageV1().StorageClasses().Get(ctx, baseSCName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to get base StorageClass %s: %v", baseSCName, err)
	}

	newSC := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: newName,
			Annotations: map[string]string{
				"cdi.kubevirt.io/clone-strategy": "snapshot",
			},
		},
		Provisioner:          baseSC.Provisioner,
		Parameters:           baseSC.Parameters,
		ReclaimPolicy:        baseSC.ReclaimPolicy,
		VolumeBindingMode:    baseSC.VolumeBindingMode,
		AllowVolumeExpansion: baseSC.AllowVolumeExpansion,
		MountOptions:         baseSC.MountOptions,
	}

	if _, err := k8s.ClientSet().StorageV1().StorageClasses().Create(ctx, newSC, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("failed to create StorageClass %s: %v", newName, err)
	}
	log.Infof("Created StorageClass %s with snapshot clone strategy", newName)

	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)
		output, err := exec.CommandContext(ctx, "kubectl", "get", "storageprofile", newName,
			"-o", "jsonpath={.status.cloneStrategy}").Output()
		if err == nil && strings.TrimSpace(string(output)) != "" {
			log.Infof("StorageProfile %s ready with cloneStrategy: %s", newName, strings.TrimSpace(string(output)))
			return newName, nil
		}
	}
	log.Warnf("StorageProfile for %s not ready after 30s, proceeding anyway", newName)
	return newName, nil
}

func deleteSnapshotCloneSC(ctx context.Context, scName string) {
	k8s := getK8SConnector()
	if err := k8s.ClientSet().StorageV1().StorageClasses().Delete(ctx, scName, metav1.DeleteOptions{}); err != nil {
		log.Warnf("Failed to delete StorageClass %s: %v", scName, err)
	} else {
		log.Infof("Deleted StorageClass %s", scName)
	}
}
