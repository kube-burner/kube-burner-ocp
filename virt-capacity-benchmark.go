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

package ocp

import (
	"fmt"
	"math"
	"os"

	k8sstorage "github.com/cloud-bulldozer/go-commons/v2/k8s-storage"
	"github.com/cloud-bulldozer/go-commons/v2/ssh"
	"github.com/cloud-bulldozer/go-commons/v2/virtctl"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

const (
	VirtCapacityBenchmarkSSHKeyFileName = "ssh"
	VirtCapacityBenchmarkTmpDirPattern  = "kube-burner-capacity-benchmark-*"
	virtCapacityBenchmarkTestName       = "virt-capacity-benchmark"
)

// NewVirtCapacityBenchmark holds the virt-capacity-benchmark workload
func NewVirtCapacityBenchmark(wh *workloads.WorkloadHelper) *cobra.Command {
	var storageClassName string
	var sshKeyPairPath string
	var maxIterations int
	var vmsPerIteration int
	var dataVolumeCount int
	var testNamespace string
	var skipMigrationJob bool
	var minimalVolumeSize int
	var minimalVolumeIncreaseSize int
	var skipResizeJob bool
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:          virtCapacityBenchmarkTestName,
		Short:        "Runs capacity-benchmark workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			var err error

			if !virtctl.IsInstalled() {
				log.Fatalf("Failed to run virtctl. Check that it is installed, in PATH and working")
			}

			k8sConnector := getK8SConnector()

			storageClassName, err = k8sstorage.GetStorageClassName(k8sConnector, storageClassName, true)
			if err != nil {
				log.Fatal(err)
			}
			supported, err := k8sstorage.StorageClassSupportsVolumeExpansion(k8sConnector, storageClassName)
			if err != nil {
				log.Fatal(err)
			}
			if !supported {
				log.Fatalf("Storage Class [%s] does not support volume expansion", storageClassName)
			}
			volumeSnapshotClassName, err := k8sstorage.GetVolumeSnapshotClassNameForStorageClass(k8sConnector, storageClassName)
			if err != nil {
				log.Fatal(err)
			}
			if volumeSnapshotClassName == "" {
				log.Fatalf("Could not find a corresponding VolumeSnapshotClass for StorageClass %s", storageClassName)
			}
			log.Infof("Running tests with Storage Class [%s]", storageClassName)
		},
		Run: func(cmd *cobra.Command, args []string) {
			privateKeyPath, publicKeyPath, err := ssh.GenerateSSHKeyPair(sshKeyPairPath, VirtCapacityBenchmarkTmpDirPattern, VirtCapacityBenchmarkSSHKeyFileName)
			if err != nil {
				log.Fatalf("Failed to generate SSH keys for the test - %v", err)
			}

			rootVolumeSize := 6
			dataVolumeSize := 1
			if minimalVolumeSize != 0 {
				rootVolumeSize = int(math.Max(float64(rootVolumeSize), float64(minimalVolumeSize)))
				dataVolumeSize = int(math.Max(float64(dataVolumeSize), float64(minimalVolumeSize)))
			}

			volumeSizeIncrement := 1
			if minimalVolumeIncreaseSize != 0 {
				volumeSizeIncrement = int(math.Max(float64(volumeSizeIncrement), float64(minimalVolumeIncreaseSize)))
			}

			if skipMigrationJob {
				log.Infof("skipMigrationJob is set to true")
			}
			if skipResizeJob {
				log.Infof("skipResizeJob is set to true")
			}

			additionalVars := map[string]interface{}{
				"privateKey":          privateKeyPath,
				"publicKey":           publicKeyPath,
				"vmCount":             fmt.Sprint(vmsPerIteration),
				"storageClassName":    storageClassName,
				"testNamespace":       testNamespace,
				"dataVolumeCounters":  generateLoopCounterSlice(dataVolumeCount),
				"skipMigrationJob":    skipMigrationJob,
				"rootVolumeSize":      rootVolumeSize,
				"dataVolumeSize":      dataVolumeSize,
				"volumeSizeIncrement": volumeSizeIncrement,
				"skipResizeJob":       skipResizeJob,
			}

			setMetrics(cmd, metricsProfiles)

			log.Infof("Running tests in Namespace [%s]", testNamespace)
			counter := 0
			for {
				os.Setenv("counter", fmt.Sprint(counter))
				rc = wh.RunWithAdditionalVars(cmd.Name(), additionalVars)
				if rc != 0 {
					log.Infof("Capacity failed in loop #%d", counter)
					break
				}
				counter += 1
				if maxIterations > 0 && counter >= maxIterations {
					log.Infof("Reached maxIterations [%d]", maxIterations)
					break
				}
			}
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVar(&storageClassName, "storage-class", "", "Name of the Storage Class to test")
	cmd.Flags().StringVar(&sshKeyPairPath, "ssh-key-path", "", "Path to save the generarated SSH keys - default to a temporary location")
	cmd.Flags().IntVar(&maxIterations, "max-iterations", 0, "Maximum times to run the test sequence. Default - run until failure (0)")
	cmd.Flags().IntVar(&vmsPerIteration, "vms", 5, "Number of VMs to test in each iteration")
	cmd.Flags().IntVar(&dataVolumeCount, "data-volume-count", 9, "Number of data volumes per VM")
	cmd.Flags().StringVarP(&testNamespace, "namespace", "n", virtCapacityBenchmarkTestName, "Namespace to run the test in")
	cmd.Flags().BoolVar(&skipMigrationJob, "skip-migration-job", false, "Skip the migration job - use when the StorageClass does not support RWX")
	cmd.Flags().IntVar(&minimalVolumeSize, "min-vol-size", 0, "Minimal volume size - use when enforced or overridden by the StorageClass")
	cmd.Flags().IntVar(&minimalVolumeIncreaseSize, "min-vol-inc-size", 0, "Minimal volume increment size - use when enforced or overridden by the StorageClass")
	cmd.Flags().BoolVar(&skipResizeJob, "skip-resize-job", false, "Skip the resize propagation check - For now use when values are propagated in a base of 10 instead of 2")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics-aggregated.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
