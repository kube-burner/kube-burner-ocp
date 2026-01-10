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

package workloads

import (
	"os"
	"time"

	"github.com/cloud-bulldozer/go-commons/v2/ssh"
	"github.com/cloud-bulldozer/go-commons/v2/virtctl"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"

	"github.com/spf13/cobra"
)

const (
	virtCloneSSHKeyFileName = "ssh"
	virtCloneTmpDirPattern  = "kube-burner-virt-clone-*"
	virtCloneTestName       = "virt-clone"
	// Defaults
	virtCloneDefaultDataVolumeCount = 0
)

// Returns virt-density workload
func NewVirtClone(wh *workloads.WorkloadHelper) *cobra.Command {
	var storageClassName string
	var volumeSnapshotClassName string
	var sshKeyPairPath string
	var useSnapshot bool
	var iterations int
	var clonesPerIteration int
	var testNamespaceBaseName string
	var metricsProfiles []string
	var volumeAccessMode string
	var verifyEachIteration bool
	var jobIterationDelay time.Duration
	var verifyMaxWaitTime time.Duration
	var dataVolumeCount int
	var rc int
	cmd := &cobra.Command{
		Use:          virtCloneTestName,
		Short:        "Runs virt-clone workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			if _, ok := accessModeTranslator[volumeAccessMode]; !ok {
				log.Fatalf("Unsupported access mode - %s", volumeAccessMode)
			}

			if !virtctl.IsInstalled() {
				log.Fatalf("Failed to run virtctl. Check that it is installed, in PATH and working")
			}

			storageClassName, volumeSnapshotClassName = getStorageAndSnapshotClasses(storageClassName, useSnapshot, cmd.Flags().Lookup("use-snapshot").Changed)
		},
		Run: func(cmd *cobra.Command, args []string) {
			privateKeyPath, publicKeyPath, err := ssh.GenerateSSHKeyPair(sshKeyPairPath, virtCloneTmpDirPattern, virtCloneSSHKeyFileName)
			if err != nil {
				log.Fatalf("Failed to generate SSH keys for the test - %v", err)
			}

			AdditionalVars["privateKey"] = privateKeyPath
			AdditionalVars["publicKey"] = publicKeyPath
			AdditionalVars["storageClassName"] = storageClassName
			AdditionalVars["volumeSnapshotClassName"] = volumeSnapshotClassName
			AdditionalVars["testNamespaceBaseName"] = testNamespaceBaseName
			AdditionalVars["accessMode"] = accessModeTranslator[volumeAccessMode]
			AdditionalVars["iterations"] = iterations
			AdditionalVars["clonesPerIteration"] = clonesPerIteration
			AdditionalVars["verifyEachIteration"] = verifyEachIteration
			AdditionalVars["jobIterationDelay"] = jobIterationDelay
			AdditionalVars["verifyMaxWaitTime"] = verifyMaxWaitTime
			AdditionalVars["dataVolumeCounters"] = generateLoopCounterSlice(dataVolumeCount, 1)

			setMetrics(cmd, metricsProfiles)
			wh.SetVariables(AdditionalVars, nil)
			rc = wh.Run(cmd.Name() + ".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVar(&storageClassName, "storage-class", "", "Name of the Storage Class to test")
	cmd.Flags().StringVar(&sshKeyPairPath, "ssh-key-path", "", "Path to save the generarated SSH keys")
	cmd.Flags().BoolVar(&useSnapshot, "use-snapshot", true, "Clone from snapshot")
	cmd.Flags().IntVar(&iterations, "iterations", 1, "Number of iterations to create VirtualMachines. The total number of VirtualMachines is iterations*iteration-clones")
	cmd.Flags().IntVar(&clonesPerIteration, "iteration-clones", 10, "How many VirtualMachines to create per iteration. The total number of VirtualMachines is iterations*iteration-clones")
	cmd.Flags().StringVarP(&testNamespaceBaseName, "namespace", "n", virtCloneTestName, "Base name for the namespace to run the test in")
	cmd.Flags().StringVar(&volumeAccessMode, "access-mode", "RWX", "Access mode for the created volumes - RO, RWO, RWX")
	cmd.Flags().BoolVar(&verifyEachIteration, "verify-each-iteration", true, "Wait for each iteration to complete and verify before starting the next one")
	cmd.Flags().DurationVar(&jobIterationDelay, "job-iteration-delay", 0, "Delay between job iterations")
	cmd.Flags().DurationVar(&verifyMaxWaitTime, "verify-max-wait-time", 1*time.Hour, "Max wait time for clone creation waiting")
	cmd.Flags().IntVar(&dataVolumeCount, "data-volume-count", virtCloneDefaultDataVolumeCount, "Number of data volumes per VM")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
