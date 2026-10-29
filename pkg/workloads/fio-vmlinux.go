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

	"github.com/cloud-bulldozer/go-commons/v2/ssh"
	"github.com/cloud-bulldozer/go-commons/v2/virtctl"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	fioVMLinuxTestName         = "fio-vmlinux"
	fioVMLinuxJobName          = "create-fio-vmlinux-vms"
	fioVMLinuxResultPath       = "/root/testdir/fio.json"
	fioVMLinuxCollectExtra     = 30 * time.Minute
	fioVMLinuxSSHTmpDirPattern = "kube-burner-fio-vmlinux-ssh-*"
	fioVMLinuxSSHKeyFileName   = "ssh"
	fioVMLinuxDefaultSourceDV  = "fio-vmlinux-source-dv"
)

var (
	fioVMLinuxNamespaceLabelSelector = fmt.Sprintf("%s=%s", kubeBurnerTestNameLabelKey, fioVMLinuxTestName)
)

// NewFioVMLinux holds the fio-vmlinux workload (Linux VM fio)
func NewFioVMLinux(wh *workloads.WorkloadHelper) *cobra.Command {
	var storageClassName string
	var vmImage, vmCPU, vmMemory, rootVolumeSize, sourceDVName string
	var vms int
	var testNamespace string
	var volumeAccessMode string
	var selector string
	var metricsProfiles []string
	var fioRuntime, fioSize, fioIoengine, fioIodepth string
	var fioRw, fioBs []string
	var collectFioResults bool
	var sshUsername, sshPassword, sshKeyPairPath string
	var collectTimeout time.Duration
	var cleanup, perNodeDV bool
	var createdSCName string
	var rc int
	cmd := &cobra.Command{
		Use:          fioVMLinuxTestName,
		Short:        "Runs fio-vmlinux workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			if cleanup {
				return
			}
			if _, ok := accessModeTranslator[volumeAccessMode]; !ok {
				log.Fatalf("Unsupported access mode - %s", volumeAccessMode)
			}
			if !virtctl.IsInstalled() {
				log.Fatal("Failed to run virtctl. Check that it is installed, in PATH and working (required to start fio via SSH)")
			}
			storageClassName, _ = getStorageAndSnapshotClasses(storageClassName, false, true)
			if perNodeDV {
				scName, err := createSnapshotCloneSC(context.Background(), storageClassName)
				if err != nil {
					log.Fatalf("Failed to create snapshot clone StorageClass: %v", err)
				}
				createdSCName = scName
				storageClassName = scName
			}
		},
		Run: func(cmd *cobra.Command, args []string) {
			if cleanup {
				log.Infof("Cleaning up all the resources from the previous run")
				cleanupTestNamespaces(cmd.Context(), fioVMLinuxNamespaceLabelSelector)
				return
			}

			var err error
			wh.SummaryMetadata["OCPVirtualizationVersion"], err = wh.MetadataAgent.GetOCPVirtualizationVersion()
			if err != nil {
				log.Warnf("Failed to get OCP Virtualization version: %v", err)
			}

			nodeSelectorJSON, err := buildNodeSelectorJSON(selector)
			if err != nil {
				log.Fatal(err.Error())
			}

			k8s := getK8SConnector()
			workerList, err := k8s.ClientSet().CoreV1().Nodes().List(cmd.Context(), metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/worker"})
			if err != nil {
				log.Fatalf("Failed to list worker nodes: %v", err)
			}
			workerNames := make([]string, 0, len(workerList.Items))
			for _, n := range workerList.Items {
				workerNames = append(workerNames, n.Name)
			}
			sort.Strings(workerNames)
			if len(workerNames) == 0 {
				log.Fatal("No worker nodes found")
			}

			AdditionalVars["VM_IMAGE"] = vmImage
			AdditionalVars["sourceDVName"] = sourceDVName
			AdditionalVars["perNodeDV"] = perNodeDV
			AdditionalVars["workerNodes"] = strings.Join(workerNames, " ")
			AdditionalVars["workerCount"] = len(workerNames)
			AdditionalVars["VM_CPU"] = vmCPU
			AdditionalVars["VM_MEMORY"] = vmMemory
			AdditionalVars["storageClassName"] = storageClassName
			AdditionalVars["accessMode"] = accessModeTranslator[volumeAccessMode]
			AdditionalVars["vms"] = vms
			AdditionalVars["testNamespace"] = testNamespace
			AdditionalVars["NODE_SELECTOR"] = nodeSelectorJSON
			AdditionalVars["rootVolumeSize"] = rootVolumeSize
			bsList := normalizeFioList(fioBs)
			rwList := normalizeFioList(fioRw)
			if len(bsList) == 0 {
				bsList = []string{"4k"}
			}
			if len(rwList) == 0 {
				rwList = []string{"write"}
			}

			AdditionalVars["FIO_RUNTIME"] = fioRuntime
			AdditionalVars["FIO_SIZE"] = fioSize
			AdditionalVars["FIO_RW"] = rwList[0]
			AdditionalVars["FIO_IOENGINE"] = fioIoengine
			AdditionalVars["FIO_IODEPTH"] = fioIodepth
			AdditionalVars["FIO_BS"] = bsList[0]
			AdditionalVars["SSH_AUTHORIZED_KEY"] = ""

			sshPrivateKeyPath, err := prepareFioVMLinuxSSHKey(sshKeyPairPath)
			if err != nil {
				log.Fatalf("Failed to prepare SSH key for Linux fio: %v", err)
			}

			userGC := AdditionalVars["GC"]
			// Keep VMs until SSH-start + collect finish.
			AdditionalVars["GC"] = false

			setMetrics(cmd, metricsProfiles)
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")

			if rc == 0 || rc == 3 {
				sshOpts := fioCollectOpts{
					Namespace:       testNamespace,
					OS:              "linux",
					JobName:         fioVMLinuxJobName,
					ResultPath:      fioVMLinuxResultPath,
					SSHUsername:     sshUsername,
					SSHPassword:     "", // key auth; sshpass cannot drive virtctl password prompts reliably
					SSHIdentityFile: sshPrivateKeyPath,
					StorageClass:    storageClassName,
					FioRuntime:      fioRuntime,
					FioSize:         fioSize,
					FioIoengine:     fioIoengine,
					FioIodepth:      fioIodepth,
				}

				if err := waitForFioSSH(cmd.Context(), sshOpts, fioSSHWaitTimeout); err != nil {
					log.Errorf("Waiting for guest SSH failed: %v", err)
					rc = 1
				} else {
					timeout := collectTimeout
					if timeout <= 0 {
						timeout = fioCollectTimeout(fioRuntime, fioVMLinuxCollectExtra)
					}
					if err := runFioMatrix(cmd.Context(), wh, sshOpts, bsList, rwList, runLinuxFioViaSSH, "run-fio-vmlinux", collectFioResults, timeout); err != nil {
						log.Errorf("fio matrix failed: %v", err)
						rc = 1
					}
				}

				if shouldCleanupAfterFioCollect(userGC) {
					cleanupTestNamespaces(cmd.Context(), fioVMLinuxNamespaceLabelSelector)
				}
			}
			if createdSCName != "" {
				deleteSnapshotCloneSC(context.Background(), createdSCName)
			}
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVar(&storageClassName, "storage-class", "", "Name of the Storage Class to test")
	cmd.Flags().IntVar(&vms, "vms", 1, "Number of VirtualMachines to create")
	cmd.Flags().StringVarP(&testNamespace, "namespace", "n", fioVMLinuxTestName, "Namespace to run the test in")
	cmd.Flags().StringVar(&vmImage, "vm-image", "quay.io/containerdisks/fedora:41", "Linux VM image to be deployed")
	cmd.Flags().StringVar(&sourceDVName, "source-dv-name", fioVMLinuxDefaultSourceDV, "Name prefix of the source DataVolume for PVC cloning")
	cmd.Flags().BoolVar(&perNodeDV, "per-node-dv", false, "Create one source DV per worker node (required for node-local storage like LVMS)")
	cmd.Flags().StringVar(&vmCPU, "vm-cpu", "1", "Number of CPU cores for the VM")
	cmd.Flags().StringVar(&vmMemory, "vm-memory", "512Mi", "Amount of memory for the VM")
	cmd.Flags().StringVar(&rootVolumeSize, "root-volume-size", "10Gi", "Size of the VM root volume")
	cmd.Flags().StringVar(&volumeAccessMode, "access-mode", "RWX", "Access mode for the created volumes - RO, RWO, RWX")
	cmd.Flags().StringVar(&selector, "selector", WorkerNodeSelector, "Node selector")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().StringVar(&fioRuntime, "fio-runtime", "60s", "fio --runtime value (guest run started via SSH after Ready)")
	cmd.Flags().StringVar(&fioSize, "fio-size", "1G", "fio --size value")
	cmd.Flags().StringSliceVar(&fioRw, "fio-rw", []string{"write"}, "fio --rw value(s); repeat or CSV (e.g. --fio-rw=write --fio-rw=read). Cartesian with --fio-bs on same VMs")
	cmd.Flags().StringVar(&fioIoengine, "fio-ioengine", "libaio", "fio --ioengine value (fio default is psync if unset)")
	cmd.Flags().StringVar(&fioIodepth, "fio-iodepth", "16", "fio --iodepth value (libaio needs >1 to queue I/O)")
	cmd.Flags().StringSliceVar(&fioBs, "fio-bs", []string{"4k"}, "fio --bs value(s); repeat or CSV (e.g. --fio-bs=4k --fio-bs=1024k). Cartesian with --fio-rw on same VMs")
	cmd.Flags().BoolVar(&collectFioResults, "collect-fio-results", true, "After each matrix combo, wait for guest fio.json, flatten, and Index as metricName fioResult (forces GC=false during create)")
	cmd.Flags().StringVar(&sshUsername, "ssh-username", "root", "Guest SSH username for fio start/collect (root + /root/testdir; works across RPM containerdisks)")
	cmd.Flags().StringVar(&sshPassword, "ssh-password", "fedora", "Unused for Linux collect (key auth); optional console login via cloud-init root password")
	cmd.Flags().StringVar(&sshKeyPairPath, "ssh-key-path", "", "Directory to store generated SSH key pair used to start fio and collect results")
	cmd.Flags().DurationVar(&collectTimeout, "collect-timeout", 0, "Timeout waiting for guest fio.json (default: fio-runtime + 30m)")
	cmd.Flags().BoolVar(&cleanup, "cleanup", false, "Cleanup resources created by previous runs")
	return cmd
}

func prepareFioVMLinuxSSHKey(sshKeyPairPath string) (string, error) {
	privateKeyPath, publicKeyPath, err := ssh.GenerateSSHKeyPair(sshKeyPairPath, fioVMLinuxSSHTmpDirPattern, fioVMLinuxSSHKeyFileName)
	if err != nil {
		return "", err
	}
	pub, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return "", fmt.Errorf("read public key %s: %w", publicKeyPath, err)
	}
	AdditionalVars["SSH_AUTHORIZED_KEY"] = strings.TrimSpace(string(pub))
	log.Infof("Injecting SSH public key for Linux fio (private key: %s)", privateKeyPath)
	return privateKeyPath, nil
}
