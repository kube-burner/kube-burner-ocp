// Copyright 2024 The Kube-burner Authors.
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
	"os"

	k8sstorage "github.com/cloud-bulldozer/go-commons/v2/k8s-storage"
	"github.com/cloud-bulldozer/go-commons/v2/ssh"
	"github.com/cloud-bulldozer/go-commons/v2/virtctl"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"

	"github.com/spf13/cobra"
)

const (
	virtCloneSSHKeyFileName = "ssh"
	virtCloneTmpDirPattern  = "kube-burner-virt-clone-*"
	virtCloneTestName       = "virt-clone"
)

// Returns virt-density workload
func NewVirtClone(wh *workloads.WorkloadHelper) *cobra.Command {
	var storageClassName string
	var volumeSnapshotClassName string
	var sshKeyPairPath string
	var useSnapshot bool
	var vmCount int
	var testNamespaceBaseName string
	var metricsProfiles []string
	var volumeAccessMode string
	var rc int
	cmd := &cobra.Command{
		Use:          virtCloneTestName,
		Short:        "Runs virt-clone workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			var err error

			if _, ok := accessModeTranslator[volumeAccessMode]; !ok {
				log.Fatalf("Unsupported access mode - %s", volumeAccessMode)
			}

			if !virtctl.IsInstalled() {
				log.Fatalf("Failed to run virtctl. Check that it is installed, in PATH and working")
			}

			k8sConnector := getK8SConnector()

			// Verify provided storage class name or get default of cluster
			storageClassName, err = k8sstorage.GetStorageClassName(k8sConnector, storageClassName, true)
			if err != nil {
				log.Fatal(err)
			}
			log.Infof("Running tests with Storage Class [%s]", storageClassName)

			// If user did not set use-snapshot, get the value from the StorageProfile
			if !cmd.Flags().Lookup("use-snapshot").Changed {
				sourceFormat, err := k8sstorage.GetDataImportCronSourceFormatForStorageClass(k8sConnector, storageClassName)
				if err != nil {
					log.Fatalf("Failed to get source format for StorageClass [%s] - %v", storageClassName, err)
				}
				useSnapshot = sourceFormat == "snapshot"
				log.Info("The flag use-snapshot was not set. Using the value from the StorageProfile: ", useSnapshot)
			}

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
		},
		Run: func(cmd *cobra.Command, args []string) {
			privateKeyPath, publicKeyPath, err := ssh.GenerateSSHKeyPair(sshKeyPairPath, virtCloneTmpDirPattern, virtCloneSSHKeyFileName)
			if err != nil {
				log.Fatalf("Failed to generate SSH keys for the test - %v", err)
			}

			additionalVars := map[string]interface{}{
				"privateKey":              privateKeyPath,
				"publicKey":               publicKeyPath,
				"storageClassName":        storageClassName,
				"volumeSnapshotClassName": volumeSnapshotClassName,
				"testNamespaceBaseName":   testNamespaceBaseName,
				"useSnapshot":             useSnapshot,
				"cloneVMCount":            vmCount,
				"accessMode":              accessModeTranslator[volumeAccessMode],
			}

			setMetrics(cmd, metricsProfiles)
			rc = wh.RunWithAdditionalVars(cmd.Name(), additionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVar(&storageClassName, "storage-class", "", "Name of the Storage Class to test")
	cmd.Flags().StringVar(&sshKeyPairPath, "ssh-key-path", "", "Path to save the generarated SSH keys")
	cmd.Flags().BoolVar(&useSnapshot, "use-snapshot", true, "Clone from snapshot")
	cmd.Flags().IntVar(&vmCount, "vms", 10, "Number of clone VMs to create")
	cmd.Flags().StringVarP(&testNamespaceBaseName, "namespace", "n", virtCloneTestName, "Base name for the namespace to run the test in")
	cmd.Flags().StringVar(&volumeAccessMode, "access-mode", "RWX", "Access mode for the created volumes - RO, RWO, RWX")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
