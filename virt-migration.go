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
	"fmt"
	"os"

	"github.com/cloud-bulldozer/go-commons/v2/ssh"
	"github.com/cloud-bulldozer/go-commons/v2/virtctl"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"

	"github.com/spf13/cobra"
)

const (
	virtMigrationSSHKeyFileName = "ssh"
	virtMigrationTmpDirPattern  = "kube-burner-virt-migration-*"
	virtMigrationTestName       = "virt-migration"
	// Defaults
	virtMigrationDefaultDataVolumeCount     = 1
	virtMigrationDefaultVMsPerIteration     = 10
	virtMigrationDefaultIteration           = 2
	virtMigrationDefaultLoadVMsIteration    = 0
	virtMigrationDefaultLoadVMsPerIteration = 0
	virtMigrationDefaultMigrationQPS        = 20
)

// Returns virt-density workload
func NewVirtMigration(wh *workloads.WorkloadHelper) *cobra.Command {
	var storageClassName string
	var sshKeyPairPath string
	var iterations int
	var vmsPerIteration int
	var testNamespace string
	var dataVolumeCount int
	var workerNodeName string
	var metricsProfiles []string
	var loadVMsIterations int
	var loadVMsPerIteration int
	var migrationQPS int
	var udnLayer2 bool
	var udnSubnets string

	var rc int
	cmd := &cobra.Command{
		Use:          virtMigrationTestName,
		Short:        fmt.Sprintf("Runs %s workload", virtMigrationTestName),
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			if !virtctl.IsInstalled() {
				log.Fatalf("Failed to run virtctl. Check that it is installed, in PATH and working")
			}

			storageClassName, _ = getStorageAndSnapshotClasses(storageClassName, false, true)

			workerNodeName = verifyOrGetRandomWorkerNodeName(workerNodeName)
			log.Infof("Test will schedule on and migrate from worker node [%v]", workerNodeName)
		},
		Run: func(cmd *cobra.Command, args []string) {
			privateKeyPath, publicKeyPath, err := ssh.GenerateSSHKeyPair(sshKeyPairPath, virtMigrationTmpDirPattern, virtMigrationSSHKeyFileName)
			if err != nil {
				log.Fatalf("Failed to generate SSH keys for the test - %v", err)
			}

			AdditionalVars["privateKey"] = privateKeyPath
			AdditionalVars["publicKey"] = publicKeyPath
			AdditionalVars["storageClassName"] = storageClassName
			AdditionalVars["udnLayer2"] = udnLayer2
			AdditionalVars["udnSubnets"] = udnSubnets
			AdditionalVars["testNamespace"] = testNamespace
			AdditionalVars["vmCreateIterations"] = iterations
			AdditionalVars["vmCreatePerIteration"] = vmsPerIteration
			AdditionalVars["dataVolumeCounters"] = generateLoopCounterSlice(dataVolumeCount, 1)
			AdditionalVars["workerNodeName"] = workerNodeName
			AdditionalVars["loadVMsIterations"] = loadVMsIterations
			AdditionalVars["loadVMsPerIteration"] = loadVMsPerIteration
			AdditionalVars["migrationQPS"] = migrationQPS

			setMetrics(cmd, metricsProfiles)
			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVar(&workerNodeName, "worker-node", "", "Name of the Worker Node to schedule and migrate from. If not set, a random one is used")
	cmd.Flags().StringVar(&storageClassName, "storage-class", "", "Name of the Storage Class to test")
	cmd.Flags().StringVar(&sshKeyPairPath, "ssh-key-path", "", "Path to save the generarated SSH keys")
	cmd.Flags().StringVarP(&testNamespace, "namespace", "n", virtMigrationTestName, "Name for the namespace to run the test in")
	cmd.Flags().IntVar(&iterations, "iterations", virtMigrationDefaultIteration, "How many iterations of VM creations. The total number of VMs is iterations*iteration-vms")
	cmd.Flags().IntVar(&vmsPerIteration, "iteration-vms", virtMigrationDefaultVMsPerIteration, "How many VMs to create in each iteration. The total number of VMs is iterations*iteration-vms")
	cmd.Flags().IntVar(&dataVolumeCount, "data-volume-count", virtMigrationDefaultDataVolumeCount, "Number of data volumes per VM")
	cmd.Flags().IntVar(&loadVMsIterations, "load-iterations", virtMigrationDefaultLoadVMsIteration, "Number of iterations to create load VMs")
	cmd.Flags().IntVar(&loadVMsPerIteration, "load-per-iteration", virtMigrationDefaultLoadVMsPerIteration, "Number of VMs to create in each load VM iteration")
	cmd.Flags().IntVar(&migrationQPS, "migration-qps", virtMigrationDefaultMigrationQPS, "Number of concurrent calls to migrate")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.Flags().BoolVar(&udnLayer2, "udn-layer-2", false, "Enable UDN Layer 2")
	cmd.Flags().StringVar(&udnSubnets, "udn-subnets", "[\"10.132.0.0/24\"]", "UDN only -- Subnets used by the L2 UDN")
	return cmd
}
