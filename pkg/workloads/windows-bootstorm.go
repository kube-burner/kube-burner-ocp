// Copyright 2025 The Kube-burner Authors.
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
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloud-bulldozer/go-commons/v2/indexers"
	"github.com/cloud-bulldozer/go-commons/v2/virtctl"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	storagev1 "k8s.io/api/storage/v1"

	"github.com/spf13/cobra"
)

const (
	defaultBootstormNamespace = "windows-bootstorm"
	bootstormJobLabel         = "create-windows-vms"
	sshMaxRetries         = 200
	sshPollInterval       = 3 * time.Second
	dvMaxRetries          = 2400
	defaultSSHConcurrency = 20
)

var bootstormNamespace = defaultBootstormNamespace

type bootstormResult struct {
	VMName        string
	Node          string
	BootstormTime float64
	TotalRunTime  float64
	AccessVM      int
}

func NewWindowsBootstorm(wh *workloads.WorkloadHelper) *cobra.Command {
	var windowsImageURL, storageClassName, volumeAccessMode, vmMemory, vmMemoryLimit, storageSize, cdiSourceType, cdiSourceS3Cred, nodeDistribution, sourceDVName, namespace, evictionStrategy string
	var vmsPerNode, vmCPU, vmCPULimit, vmCPURequest int
	var perNodeDV, clearNodeCaches bool
	var createdSCName string
	var sshConcurrencyLimit int
	var bulkSleepTime time.Duration
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "windows-bootstorm",
		Short:        "Runs windows-bootstorm workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			if windowsImageURL == "" {
				log.Fatal("--windows-image-url is required")
			}
			if _, ok := accessModeTranslator[volumeAccessMode]; !ok {
				log.Fatalf("Unsupported access mode - %s", volumeAccessMode)
			}
			if !virtctl.IsInstalled() {
				log.Fatal("Failed to run virtctl. Check that it is installed, in PATH and working")
			}
			if nodeDistribution != "round-robin" && nodeDistribution != "sequential" {
				log.Fatalf("Unsupported node-distribution: %s (use round-robin or sequential)", nodeDistribution)
			}
			if vmCPURequest == 0 {
				vmCPURequest = vmCPU
			}
			bootstormNamespace = namespace
			storageClassName, _ = getStorageAndSnapshotClasses(storageClassName, false, true)
			if perNodeDV {
				scName, err := createSnapshotCloneSC(context.Background(), storageClassName)
				if err != nil {
					log.Fatalf("Failed to create snapshot clone StorageClass: %v", err)
				}
				createdSCName = scName
				storageClassName = scName
			}
			// Clean up any leftover namespace from a crashed previous run
			cleanupTestNamespaces(context.Background(), "kube-burner.io/test-name="+namespace)
			if clearNodeCaches {
				clearWorkerNodeCaches()
			}
		},
		Run: func(cmd *cobra.Command, args []string) {
			k8s := getK8SConnector()
			workerList, err := k8s.ClientSet().CoreV1().Nodes().List(context.Background(), metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/worker"})
			if err != nil {
				log.Fatalf("Failed to list worker nodes: %v", err)
			}
			workerNames := make([]string, 0, len(workerList.Items))
			for _, n := range workerList.Items {
				workerNames = append(workerNames, n.Name)
			}
			sort.Strings(workerNames)
			totalVMs := len(workerNames) * vmsPerNode
			if totalVMs == 0 {
				log.Fatal("No worker nodes found or vms-per-node is 0")
			}

			AdditionalVars["JOB_ITERATIONS"] = totalVMs
			AdditionalVars["windowsImageURL"] = windowsImageURL
			AdditionalVars["storageClassName"] = storageClassName
			AdditionalVars["accessMode"] = accessModeTranslator[volumeAccessMode]
			AdditionalVars["vmMemory"] = vmMemory
			AdditionalVars["vmCPU"] = vmCPU
			AdditionalVars["vmCPURequest"] = vmCPURequest
			AdditionalVars["vmCPULimit"] = vmCPULimit
			AdditionalVars["vmMemoryLimit"] = vmMemoryLimit
			AdditionalVars["storageSize"] = storageSize
			AdditionalVars["cdiSourceType"] = cdiSourceType
			AdditionalVars["cdiSourceS3Cred"] = cdiSourceS3Cred
			AdditionalVars["sourceDVName"] = sourceDVName
			AdditionalVars["perNodeDV"] = perNodeDV
			AdditionalVars["namespace"] = namespace
			AdditionalVars["evictionStrategy"] = evictionStrategy
			AdditionalVars["workerNodes"] = strings.Join(workerNames, " ")
			AdditionalVars["workerCount"] = len(workerNames)
			AdditionalVars["nodeDistribution"] = nodeDistribution
			AdditionalVars["vmsPerNode"] = vmsPerNode

			setMetrics(cmd, metricsProfiles)
			AddVirtMetadata(wh, windowsImageURL, "", "")
			AdditionalVars["GC"] = false
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")

			if rc == 0 {
				results := startAndMeasureVMs(cmd.Context(), bulkSleepTime, sshConcurrencyLimit)
				if len(results) == 0 {
					log.Error("Bootstorm measurement produced no results")
					rc = 1
				} else {
					failed := 0
					for _, r := range results {
						if r.AccessVM == 0 {
							failed++
						}
					}
					if failed > 0 {
						log.Warnf("%d/%d VMs failed SSH accessibility check", failed, len(results))
					}
					if failed == len(results) {
						log.Error("All VMs failed SSH accessibility check")
						rc = 1
					} else {
						writeBootstormResults(results, wh, windowsImageURL, totalVMs)
					}
				}
			}
			stopAndDeleteBootstormVMs(context.Background())
			deleteBootstormDVs(context.Background())
			cleanupTestNamespaces(context.Background(), "kube-burner.io/test-name="+namespace)
			if createdSCName != "" {
				deleteSnapshotCloneSC(context.Background(), createdSCName)
			}
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVar(&windowsImageURL, "windows-image-url", "", "HTTP URL to Windows QCOW2 disk image")
	cmd.Flags().IntVar(&vmsPerNode, "vms-per-node", 1, "Number of Windows VMs to create per worker node")
	cmd.Flags().StringVar(&storageClassName, "storage-class", "", "Storage class for DataVolumes (auto-detected if empty)")
	cmd.Flags().StringVar(&volumeAccessMode, "access-mode", "RWX", "PVC access mode: RO, RWO, RWX")
	cmd.Flags().StringVar(&vmMemory, "vm-memory", "2G", "Memory request per VM")
	cmd.Flags().StringVar(&vmMemoryLimit, "vm-memory-limit", "4G", "Memory limit per VM")
	cmd.Flags().IntVar(&vmCPU, "vm-cpu", 1, "CPU cores per VM")
	cmd.Flags().IntVar(&vmCPURequest, "vm-cpu-request", 0, "CPU request per VM (defaults to --vm-cpu if unset)")
	cmd.Flags().IntVar(&vmCPULimit, "vm-cpu-limit", 2, "CPU limit per VM")
	cmd.Flags().StringVar(&storageSize, "storage-size", "76Gi", "Root disk storage size")
	cmd.Flags().StringVar(&cdiSourceType, "cdi-source-type", "http", "CDI source type: http or s3")
	cmd.Flags().StringVar(&cdiSourceS3Cred, "cdi-source-s3-cred", "", "Secret name for S3 CDI source authentication")
	cmd.Flags().StringVar(&sourceDVName, "source-dv-name", "windows-bootstorm-source-dv", "Name of the source DataVolume for PVC cloning")
	cmd.Flags().StringVar(&namespace, "namespace", defaultBootstormNamespace, "Namespace for bootstorm VMs and DataVolumes")
	cmd.Flags().StringVar(&evictionStrategy, "eviction-strategy", "LiveMigrate", "VM eviction strategy: LiveMigrate or None")
	cmd.Flags().BoolVar(&perNodeDV, "per-node-dv", false, "Create one source DV per worker node (required for node-local storage like LVMS)")
	cmd.Flags().BoolVar(&clearNodeCaches, "clear-node-caches", true, "Clear worker node buffer caches before the benchmark")
	cmd.Flags().DurationVar(&bulkSleepTime, "bulk-sleep", 30*time.Second, "Rest duration between VM start bulks")
	cmd.Flags().StringVar(&nodeDistribution, "node-distribution", "sequential", "VM node assignment strategy: sequential or round-robin")
	cmd.Flags().IntVar(&sshConcurrencyLimit, "ssh-concurrency-limit", defaultSSHConcurrency, "Number of concurrent SSH measurement threads")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
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
	// Sort numerically by iteration number (e.g., windows-bootstorm-0-1, windows-bootstorm-1-1, ...)
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
	// Wait for all DV clones to complete
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

	// Start + measure in bulks with barrier and rest between bulks
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
	// Wait for Running
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

	// Check SSH via virtctl ssh
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

	// Phase 1: Stop all VMs
	log.Infof("Stopping %d VMs", len(vmList.Items))
	for _, vm := range vmList.Items {
		_ = exec.CommandContext(ctx, "virtctl", "stop", vm.GetName(), "-n", bootstormNamespace).Run()
	}

	// Phase 2: Wait for all VMIs to terminate (blocking)
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
	// Phase 3: Delete all VMs
	log.Infof("Deleting %d VMs", len(vmList.Items))
	for _, vm := range vmList.Items {
		_ = k8sConnector.DynamicClient().Resource(vmGVR).Namespace(bootstormNamespace).Delete(ctx, vm.GetName(), metav1.DeleteOptions{})
	}

	// Phase 4: Wait for all VMs to be fully gone (blocking)
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

	// Check if it already exists (idempotent)
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

	// Wait for CDI to create the StorageProfile
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
