// Copyright 2026 The Kube-burner Authors.
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
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cloud-bulldozer/go-commons/v2/virtctl"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/spf13/cobra"
)

const (
	defaultBootstormNamespace = "windows-bootstorm"
	bootstormJobLabel         = "create-windows-vms"
	sshMaxRetries             = 200
	sshPollInterval           = 3 * time.Second
	dvMaxRetries              = 2400
	defaultSSHConcurrency     = 20
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

			if rc == 0 || rc == 3 {
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
