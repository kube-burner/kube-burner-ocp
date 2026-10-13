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
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cloud-bulldozer/go-commons/v2/ssh"
	"github.com/cloud-bulldozer/go-commons/v2/virtctl"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	fioVMWindowsTestName         = "fio-vmwindows"
	fioVMWindowsJobName          = "create-fio-vmwindows-vms"
	fioVMWindowsResultPath       = "C:/testdir/fio.json"
	fioVMWindowsCollectExtra     = 60 * time.Minute
	fioVMWindowsSSHTmpDirPattern = "kube-burner-fio-vmwindows-ssh-*"
	fioVMWindowsSSHKeyFileName   = "ssh"
)

var (
	fioVMWindowsNamespaceLabelSelector = fmt.Sprintf("%s=%s", kubeBurnerTestNameLabelKey, fioVMWindowsTestName)
)

// NewFioVMWindows holds the fio-vmwindows workload (Windows VM fio)
func NewFioVMWindows(wh *workloads.WorkloadHelper) *cobra.Command {
	var storageClassName string
	var windowsImageURL, cdiSourceType, cdiSourceS3Cred string
	var vmCPU, vmMemory, rootVolumeSize string
	var vms int
	var testNamespace string
	var volumeAccessMode string
	var selector string
	var metricsProfiles []string
	var fioRuntime, fioSize, fioIoengine, fioIodepth, fioPath string
	var fioRw, fioBs []string
	var collectFioResults bool
	var sshUsername, sshPassword, sshKeyPairPath string
	var collectTimeout time.Duration
	var cleanup bool
	var rc int
	cmd := &cobra.Command{
		Use:          fioVMWindowsTestName,
		Short:        "Runs fio-vmwindows workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			if cleanup {
				return
			}
			if windowsImageURL == "" {
				log.Fatal("--windows-image-url is required")
			}
			if cdiSourceType != "http" && cdiSourceType != "s3" {
				log.Fatalf("Unsupported cdi-source-type - %s (use http or s3)", cdiSourceType)
			}
			if _, ok := accessModeTranslator[volumeAccessMode]; !ok {
				log.Fatalf("Unsupported access mode - %s", volumeAccessMode)
			}
			if !virtctl.IsInstalled() {
				log.Fatal("Failed to run virtctl. Check that it is installed, in PATH and working (required to start fio via SSH)")
			}
			storageClassName, _ = getStorageAndSnapshotClasses(storageClassName, false, true)
		},
		Run: func(cmd *cobra.Command, args []string) {
			if cleanup {
				log.Infof("Cleaning up all the resources from the previous run")
				cleanupTestNamespaces(cmd.Context(), fioVMWindowsNamespaceLabelSelector)
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

			AdditionalVars["WINDOWS_IMAGE_URL"] = windowsImageURL
			AdditionalVars["cdiSourceType"] = cdiSourceType
			AdditionalVars["cdiSourceS3Cred"] = cdiSourceS3Cred
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
			AdditionalVars["FIO_PATH"] = fioPath
			AdditionalVars["SSH_AUTHORIZED_KEY"] = ""

			sshPrivateKeyPath, err := prepareFioVMWindowsSSHKey(sshKeyPairPath)
			if err != nil {
				log.Fatalf("Failed to prepare SSH key for Windows fio: %v", err)
			}

			userGC := AdditionalVars["GC"]
			// Keep VMs until SSH-start + collect finish (same as Linux collect path).
			AdditionalVars["GC"] = false

			setMetrics(cmd, metricsProfiles)
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")

			if rc == 0 || rc == 3 {
				sshOpts := fioCollectOpts{
					Namespace:       testNamespace,
					OS:              "windows",
					JobName:         fioVMWindowsJobName,
					ResultPath:      fioVMWindowsResultPath,
					SSHUsername:     sshUsername,
					SSHPassword:     sshPassword,
					SSHIdentityFile: sshPrivateKeyPath,
					StorageClass:    storageClassName,
					FioRuntime:      fioRuntime,
					FioSize:         fioSize,
					FioIoengine:     fioIoengine,
					FioIodepth:      fioIodepth,
					FioPath:         fioPath,
				}

				if err := waitForFioSSH(cmd.Context(), sshOpts, fioSSHWaitTimeout); err != nil {
					log.Errorf("Waiting for guest SSH failed: %v", err)
					rc = 1
				} else {
					timeout := collectTimeout
					if timeout <= 0 {
						timeout = fioCollectTimeout(fioRuntime, fioVMWindowsCollectExtra)
					}
					if err := runFioMatrix(cmd.Context(), wh, sshOpts, bsList, rwList, runWindowsFioViaSSH, "run-fio-vmwindows", collectFioResults, timeout); err != nil {
						log.Errorf("fio matrix failed: %v", err)
						rc = 1
					}
				}

				if shouldCleanupAfterFioCollect(userGC) {
					cleanupTestNamespaces(cmd.Context(), fioVMWindowsNamespaceLabelSelector)
				}
			}
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVar(&windowsImageURL, "windows-image-url", "", "HTTP/S3 URL to Windows QCOW2 disk image (required)")
	cmd.Flags().StringVar(&cdiSourceType, "cdi-source-type", "http", "CDI source type: http or s3")
	cmd.Flags().StringVar(&cdiSourceS3Cred, "cdi-source-s3-cred", "", "Secret name for S3 CDI source authentication")
	cmd.Flags().StringVar(&storageClassName, "storage-class", "", "Name of the Storage Class to test")
	cmd.Flags().IntVar(&vms, "vms", 1, "Number of VirtualMachines to create")
	cmd.Flags().StringVarP(&testNamespace, "namespace", "n", fioVMWindowsTestName, "Namespace to run the test in")
	cmd.Flags().StringVar(&vmCPU, "vm-cpu", "2", "Number of CPU cores for the VM")
	cmd.Flags().StringVar(&vmMemory, "vm-memory", "4Gi", "Amount of memory for the VM")
	cmd.Flags().StringVar(&rootVolumeSize, "root-volume-size", "76Gi", "Size of the VM root volume")
	cmd.Flags().StringVar(&volumeAccessMode, "access-mode", "RWX", "Access mode for the created volumes - RO, RWO, RWX")
	cmd.Flags().StringVar(&selector, "selector", WorkerNodeSelector, "Node selector")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().StringVar(&fioRuntime, "fio-runtime", "60s", "fio --runtime value (guest run started via SSH after Ready)")
	cmd.Flags().StringVar(&fioSize, "fio-size", "1G", "fio --size value")
	cmd.Flags().StringSliceVar(&fioRw, "fio-rw", []string{"write"}, "fio --rw value(s); repeat or CSV (e.g. --fio-rw=write --fio-rw=read). Cartesian with --fio-bs on same VMs")
	cmd.Flags().StringVar(&fioIoengine, "fio-ioengine", "windowsaio", "fio --ioengine value (windowsaio for Windows)")
	cmd.Flags().StringVar(&fioIodepth, "fio-iodepth", "16", "fio --iodepth value")
	cmd.Flags().StringSliceVar(&fioBs, "fio-bs", []string{"4k"}, "fio --bs value(s); repeat or CSV (e.g. --fio-bs=4k --fio-bs=1024k). Cartesian with --fio-rw on same VMs")
	cmd.Flags().StringVar(&fioPath, "fio-path", `C:/fio/fio.exe`, "Path to fio.exe inside the Windows guest (use forward slashes; fio treats \\t as a tab)")
	cmd.Flags().BoolVar(&collectFioResults, "collect-fio-results", true, "After each matrix combo, wait for guest fio.json, flatten, and Index as metricName fioResult (forces GC=false during create)")
	cmd.Flags().StringVar(&sshUsername, "ssh-username", "Administrator", "Guest SSH username (OpenSSH required; key injected via cloud-init)")
	cmd.Flags().StringVar(&sshPassword, "ssh-password", "", "Optional password fallback for SSH (requires sshpass; key auth is preferred)")
	cmd.Flags().StringVar(&sshKeyPairPath, "ssh-key-path", "", "Directory to store generated SSH key pair used to start fio and collect results")
	cmd.Flags().DurationVar(&collectTimeout, "collect-timeout", 0, "Timeout waiting for guest fio.json (default: fio-runtime + 60m)")
	cmd.Flags().BoolVar(&cleanup, "cleanup", false, "Cleanup resources created by previous runs")
	return cmd
}

func prepareFioVMWindowsSSHKey(sshKeyPairPath string) (string, error) {
	privateKeyPath, publicKeyPath, err := ssh.GenerateSSHKeyPair(sshKeyPairPath, fioVMWindowsSSHTmpDirPattern, fioVMWindowsSSHKeyFileName)
	if err != nil {
		return "", err
	}
	pub, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return "", fmt.Errorf("read public key %s: %w", publicKeyPath, err)
	}
	AdditionalVars["SSH_AUTHORIZED_KEY"] = strings.TrimSpace(string(pub))
	log.Infof("Injecting SSH public key for Windows fio (private key: %s)", privateKeyPath)
	return privateKeyPath, nil
}
