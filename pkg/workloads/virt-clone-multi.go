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
	"time"

	"github.com/cloud-bulldozer/go-commons/v2/ssh"
	"github.com/cloud-bulldozer/go-commons/v2/virtctl"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	VirtCloneMultiSSHKeyFileName = "ssh"
	VirtCloneMultiTmpDirPattern  = "kube-burner-virt-clone-multi-*"
	virtCloneMultiTestName       = "virt-clone-multi"
)

var (
	virtCloneMultiNamespaceLabelSelector = fmt.Sprintf("%s=%s", kubeBurnerTestNameLabelKey, virtCloneMultiTestName)
)

// NewVirtCloneMulti holds the virt-clone-multi workload
func NewVirtCloneMulti(wh *workloads.WorkloadHelper) *cobra.Command {
	var storageClassName string
	var volumeSnapshotClassName string
	var sshKeyPairPath string
	var useSnapshot bool
	var namespaces int
	var iterations int
	var vmsPerIteration int
	var dataVolumeCount int
	var volumeAccessMode string
	var jobIterationDelay time.Duration
	var testNamespaceBaseName string
	var metricsProfiles []string
	var cleanupOnly bool
	var cleanup bool
	var rc int

	cmd := &cobra.Command{
		Use:          virtCloneMultiTestName,
		Short:        "Runs virt-clone-multi workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			if cleanupOnly {
				return
			}

			if _, ok := accessModeTranslator[volumeAccessMode]; !ok {
				log.Fatalf("Unsupported access mode - %s", volumeAccessMode)
			}

			if !virtctl.IsInstalled() {
				log.Fatalf("Failed to run virtctl. Check that it is installed, in PATH and working")
			}

			storageClassName, volumeSnapshotClassName = getStorageAndSnapshotClasses(storageClassName, useSnapshot, cmd.Flags().Lookup("use-snapshot").Changed)
		},
		Run: func(cmd *cobra.Command, args []string) {
			if cleanupOnly {
				log.Infof("Cleaning up all the resources from the previous run")
				cleanupTestNamespaces(cmd.Context(), virtCloneMultiNamespaceLabelSelector)
				return
			}

			privateKeyPath, publicKeyPath, err := ssh.GenerateSSHKeyPair(sshKeyPairPath, VirtCloneMultiTmpDirPattern, VirtCloneMultiSSHKeyFileName)
			if err != nil {
				log.Fatalf("Failed to generate SSH keys for the test - %v", err)
			}

			wh.SummaryMetadata["OCPVirtualizationVersion"], err = wh.MetadataAgent.GetOCPVirtualizationVersion()
			if err != nil {
				log.Warnf("Failed to get OCP Virtualization version: %v", err)
			}

			vmsPerNamespace := iterations * vmsPerIteration
			totalVMs := namespaces * vmsPerNamespace
			totalPVCs := totalVMs * (1 + dataVolumeCount) // root + data volumes

			log.Infof("Running virt-clone-multi with %d namespaces, %d iterations, %d VMs per iteration", namespaces, iterations, vmsPerIteration)
			log.Infof("Total VMs: %d (%d per namespace), Total PVCs: %d (including %d data volumes per VM)", totalVMs, vmsPerNamespace, totalPVCs, dataVolumeCount)
			log.Infof("Using Storage Class [%s], VolumeSnapshotClass [%s]", storageClassName, volumeSnapshotClassName)
			log.Infof("Use Snapshot: %t", useSnapshot)

			AdditionalVars["privateKey"] = privateKeyPath
			AdditionalVars["publicKey"] = publicKeyPath
			AdditionalVars["storageClassName"] = storageClassName
			AdditionalVars["volumeSnapshotClassName"] = volumeSnapshotClassName
			AdditionalVars["accessMode"] = accessModeTranslator[volumeAccessMode]
			AdditionalVars["useSnapshot"] = useSnapshot
			AdditionalVars["namespaces"] = namespaces
			AdditionalVars["iterations"] = iterations
			AdditionalVars["vmsPerIteration"] = vmsPerIteration
			AdditionalVars["jobIterationDelay"] = jobIterationDelay
			AdditionalVars["dataVolumeCounters"] = generateLoopCounterSlice(dataVolumeCount, 1)
			AdditionalVars["testNamespaceBaseName"] = testNamespaceBaseName

			setMetrics(cmd, metricsProfiles)

			// Run workload once - kube-burner will handle namespace iterations
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")

			if cleanup {
				log.Infof("Cleaning up all the resources from the current run")
				cleanupTestNamespaces(cmd.Context(), virtCloneMultiNamespaceLabelSelector)
			}
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}

	cmd.Flags().StringVar(&storageClassName, "storage-class", "", "Name of the Storage Class to test")
	cmd.Flags().StringVar(&sshKeyPairPath, "ssh-key-path", "", "Path to save the generated SSH keys - default to a temporary location")
	cmd.Flags().BoolVar(&useSnapshot, "use-snapshot", true, "Clone from snapshot (true) or direct PVC clone (false)")
	cmd.Flags().IntVar(&namespaces, "namespaces", 2, "Number of namespaces to create")
	cmd.Flags().IntVar(&iterations, "iterations", 5, "Number of iterations (batches) per namespace")
	cmd.Flags().IntVar(&vmsPerIteration, "vms-per-iteration", 2, "Number of VMs per iteration")
	cmd.Flags().IntVar(&dataVolumeCount, "data-volume-count", 0, "Number of additional data volumes per VM (default: 0)")
	cmd.Flags().StringVar(&volumeAccessMode, "access-mode", "RWX", "Access mode for the created volumes - RO, RWO, RWX")
	cmd.Flags().DurationVar(&jobIterationDelay, "job-iteration-delay", 1*time.Minute, "Delay between namespace iterations")
	cmd.Flags().StringVarP(&testNamespaceBaseName, "namespace", "n", virtCloneMultiTestName, "Base namespace name for the test")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics-aggregated.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().BoolVar(&cleanupOnly, "cleanup-only", false, "Only cleanup the resources created by the previous run. Do not run the test.")
	cmd.Flags().BoolVar(&cleanup, "cleanup", false, "Cleanup the resources created by the test.")

	return cmd
}
