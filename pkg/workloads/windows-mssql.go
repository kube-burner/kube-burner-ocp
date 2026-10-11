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
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cloud-bulldozer/go-commons/v2/indexers"
	"github.com/cloud-bulldozer/go-commons/v2/virtctl"
	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements/types"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/spf13/cobra"
)

const (
	defaultMssqlNamespace = "windows-mssql"
	mssqlJobLabel         = "create-mssql-vms"
)

var mssqlNamespace = defaultMssqlNamespace

func NewWindowsMssql(wh *workloads.WorkloadHelper) *cobra.Command {
	var windowsImageURL, storageClassName, volumeAccessMode, vmMemory, rootDiskSize, dataDiskSize, cdiSourceType, cdiSourceS3Cred, nodeDistribution, sourceDVName, namespace, evictionStrategy string
	var vmsPerNode, vmCPU, vmCPURequest, totalVMs int
	var dbWorkers, dbWarehouses, dbTransactions, dbMinWorkers, dbIterations int
	var dbRampup, dbRuntime string
	var perNodeDV, clearNodeCaches, useSnapshot bool
	var createdSCName, volumeSnapshotClassName string
	var sshConcurrencyLimit int
	var bulkSleepTime time.Duration
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          "windows-mssql",
		Short:        "Runs windows-mssql TPC-C benchmark workload",
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
			if useSnapshot && perNodeDV {
				log.Fatal("--use-snapshot and --per-node-dv are mutually exclusive (use-snapshot is for ODF, per-node-dv is for LVMS)")
			}
			mssqlNamespace = namespace
			storageClassName, volumeSnapshotClassName = getStorageAndSnapshotClasses(storageClassName, useSnapshot, cmd.Flags().Changed("use-snapshot"))
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
			calculatedVMs := totalVMs
			if calculatedVMs == 0 {
				calculatedVMs = len(workerNames) * vmsPerNode
			}
			if calculatedVMs == 0 {
				log.Fatal("No worker nodes found or vms-per-node is 0")
			}
			log.Infof("Creating %d VMs total", calculatedVMs)

			// Use persistent SSH key (same as PR approach)
			sshKeyPath := os.Getenv("HOME") + "/.ssh/windows-mssql-id_rsa"
			log.Infof("Using SSH key: %s", sshKeyPath)

			// Read the public key
			sshPubKeyPath := sshKeyPath + ".pub"
			sshPubKey, err := os.ReadFile(sshPubKeyPath)
			if err != nil {
				log.Fatalf("Failed to read SSH public key from %s: %v (image must have this key pre-configured)", sshPubKeyPath, err)
			}

			// Calculate number of source DVs needed
			sourceDVCount := 1
			if perNodeDV {
				if nodeDistribution == "sequential" {
					// Sequential: fill nodes one by one
					sourceDVCount = (calculatedVMs + vmsPerNode - 1) / vmsPerNode
				} else {
					// Round-robin: spread across nodes
					if calculatedVMs < len(workerNames) {
						sourceDVCount = calculatedVMs
					} else {
						sourceDVCount = len(workerNames)
					}
				}
			}
			log.Infof("Will create %d source DataVolume(s) for %d VM(s)", sourceDVCount, calculatedVMs)

			AdditionalVars["JOB_ITERATIONS"] = calculatedVMs
			AdditionalVars["sourceDVCount"] = sourceDVCount
			AdditionalVars["sshPublicKey"] = strings.TrimSpace(string(sshPubKey))
			AdditionalVars["sshKeyPath"] = sshKeyPath
			AdditionalVars["windowsImageURL"] = windowsImageURL
			AdditionalVars["storageClassName"] = storageClassName
			AdditionalVars["accessMode"] = accessModeTranslator[volumeAccessMode]
			AdditionalVars["vmMemory"] = vmMemory
			AdditionalVars["vmCPU"] = vmCPU
			AdditionalVars["vmCPURequest"] = vmCPURequest
			AdditionalVars["rootDiskSize"] = rootDiskSize
			AdditionalVars["dataDiskSize"] = dataDiskSize
			AdditionalVars["cdiSourceType"] = cdiSourceType
			AdditionalVars["cdiSourceS3Cred"] = cdiSourceS3Cred
			AdditionalVars["sourceDVName"] = sourceDVName
			AdditionalVars["perNodeDV"] = perNodeDV
			AdditionalVars["useSnapshot"] = useSnapshot
			AdditionalVars["volumeSnapshotClassName"] = volumeSnapshotClassName
			AdditionalVars["namespace"] = namespace
			AdditionalVars["evictionStrategy"] = evictionStrategy
			AdditionalVars["workerNodes"] = strings.Join(workerNames, " ")
			AdditionalVars["workerCount"] = len(workerNames)
			AdditionalVars["nodeDistribution"] = nodeDistribution
			AdditionalVars["vmsPerNode"] = vmsPerNode
			AdditionalVars["dbWorkers"] = dbWorkers
			AdditionalVars["dbWarehouses"] = dbWarehouses
			AdditionalVars["dbTransactions"] = dbTransactions
			AdditionalVars["dbMinWorkers"] = dbMinWorkers
			AdditionalVars["dbIterations"] = dbIterations
			AdditionalVars["dbRampup"] = dbRampup
			AdditionalVars["dbRuntime"] = dbRuntime

			setMetrics(cmd, metricsProfiles)
			AddVirtMetadata(wh, windowsImageURL, "", "")
			AdditionalVars["GC"] = false
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")

			if rc == 0 || rc == 3 {
				kubeClientProvider := config.NewKubeClientProvider("", "")
				labelSelector := fmt.Sprintf("%s=%s", config.KubeBurnerLabelRunID, workloads.ConfigSpec.GlobalConfig.RUNID)
				msFactory := measurements.NewMeasurementsFactory(workloads.ConfigSpec, wh.MetricsMetadata, nil)
				vmJob := workloads.ConfigSpec.Jobs[len(workloads.ConfigSpec.Jobs)-1]
				vmiJob := &config.Job{
					Name:         "mssql-vm-start",
					JobType:      config.CreationJob,
					QPS:          vmJob.QPS,
					Burst:        vmJob.Burst,
					Measurements: []types.Measurement{{Name: "vmiLatency", BaselinePhase: "vmiPending"}},
				}
				vmiMs := msFactory.NewMeasurements(vmiJob, kubeClientProvider, nil, labelSelector)
				vmiMs.Start()

				results := runBenchmarksAndCollectResults(cmd.Context(), mssqlNamespace, bulkSleepTime, sshConcurrencyLimit)

				if err := vmiMs.Stop(); err != nil {
					log.Errorf("vmiLatency stop error: %v", err)
				}
				for _, endpoint := range workloads.ConfigSpec.MetricsEndpoints {
					if endpoint.Type == "" {
						continue
					}
					idx, err := indexers.NewIndexer(endpoint.IndexerConfig)
					if err != nil {
						log.Errorf("Failed to create indexer for vmiLatency: %v", err)
						continue
					}
					vmiMs.Index("mssql-vm-start", map[string]indexers.Indexer{"default": *idx})
				}
				if len(results) == 0 {
					log.Error("MSSQL benchmark produced no results")
					rc = 1
				} else {
					failed := 0
					for _, r := range results {
						if r.Status == 0 {
							failed++
						}
					}
					if failed > 0 {
						log.Warnf("%d/%d VMs failed benchmark execution", failed, len(results))
					}
					if failed == len(results) {
						log.Error("All VMs failed benchmark execution")
						rc = 1
					} else {
						writeMssqlResults(results, wh, windowsImageURL, calculatedVMs)
					}
				}
			}
			stopAndDeleteMssqlVMs(context.Background(), mssqlNamespace)
			deleteMssqlDVs(context.Background(), mssqlNamespace)
			cleanupTestNamespaces(context.Background(), "kube-burner.io/test-name="+namespace)
			if createdSCName != "" {
				deleteSnapshotCloneSC(context.Background(), createdSCName)
			}
			// Cleanup complete
			log.Infof("Workload cleanup complete")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVar(&windowsImageURL, "windows-image-url", "", "HTTP/S3 URL to Windows QCOW2 disk image with SQL Server pre-installed")
	cmd.Flags().IntVar(&vmsPerNode, "vms-per-node", 1, "Number of MSSQL VMs to create per worker node")
	cmd.Flags().IntVar(&totalVMs, "total-vms", 0, "Total number of VMs to create (overrides vms-per-node × worker-count if set)")
	cmd.Flags().StringVar(&storageClassName, "storage-class", "", "Storage class for DataVolumes (auto-detected if empty)")
	cmd.Flags().StringVar(&volumeAccessMode, "access-mode", "RWX", "PVC access mode: RO, RWO, RWX")
	cmd.Flags().StringVar(&vmMemory, "vm-memory", "32Gi", "Memory request per VM")
	cmd.Flags().IntVar(&vmCPU, "vm-cpu", 32, "CPU cores per VM")
	cmd.Flags().IntVar(&vmCPURequest, "vm-cpu-request", 0, "CPU request per VM (defaults to --vm-cpu if unset)")
	cmd.Flags().StringVar(&rootDiskSize, "root-disk-size", "76Gi", "Root disk storage size")
	cmd.Flags().StringVar(&dataDiskSize, "data-disk-size", "76Gi", "Data disk storage size for SQL Server")
	cmd.Flags().StringVar(&cdiSourceType, "cdi-source-type", "http", "CDI source type: http or s3")
	cmd.Flags().StringVar(&cdiSourceS3Cred, "cdi-source-s3-cred", "", "Secret name for S3 CDI source authentication")
	cmd.Flags().StringVar(&sourceDVName, "source-dv-name", "windows-mssql-source-dv", "Name of the source DataVolume for PVC cloning")
	cmd.Flags().StringVar(&namespace, "namespace", defaultMssqlNamespace, "Namespace for MSSQL VMs and DataVolumes")
	cmd.Flags().StringVar(&evictionStrategy, "eviction-strategy", "LiveMigrate", "VM eviction strategy: LiveMigrate or None")
	cmd.Flags().BoolVar(&perNodeDV, "per-node-dv", false, "Create one source DV per worker node (required for node-local storage like LVMS)")
	cmd.Flags().BoolVar(&useSnapshot, "use-snapshot", false, "Use VolumeSnapshot for cloning (for ODF storage, matches benchmark-runner)")
	cmd.Flags().BoolVar(&clearNodeCaches, "clear-node-caches", true, "Clear worker node buffer caches before the benchmark")
	cmd.Flags().DurationVar(&bulkSleepTime, "bulk-sleep", 30*time.Second, "Rest duration between VM start bulks")
	cmd.Flags().StringVar(&nodeDistribution, "node-distribution", "sequential", "VM node assignment strategy: sequential or round-robin")
	cmd.Flags().IntVar(&sshConcurrencyLimit, "ssh-concurrency-limit", defaultSSHConcurrency, "Number of concurrent SSH measurement threads")
	cmd.Flags().IntVar(&dbWorkers, "db-workers", 32, "HammerDB maximum concurrent workers")
	cmd.Flags().IntVar(&dbWarehouses, "db-warehouses", 96, "TPC-C warehouse count for schema build")
	cmd.Flags().IntVar(&dbTransactions, "db-transactions", 100000, "Transaction count per test run")
	cmd.Flags().IntVar(&dbMinWorkers, "db-min-workers", 1, "Starting worker count for scaling tests")
	cmd.Flags().IntVar(&dbIterations, "db-iterations", 1, "Number of benchmark repetitions")
	cmd.Flags().StringVar(&dbRampup, "db-rampup", "1m", "Benchmark rampup time")
	cmd.Flags().StringVar(&dbRuntime, "db-runtime", "1m", "Benchmark runtime per iteration")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
