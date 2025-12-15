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
	"os"

	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"

	"github.com/spf13/cobra"
)

const (
	dvCloneTestName                = "dv-clone"
	dvCloneDefaultContainerDiskUrl = "quay.io/kube-burner/tiny_image:latest"
	dvCloneDefaultDataVolumeSize   = "1Gi"
)

// Returns virt-density workload
func NewDVClone(wh *workloads.WorkloadHelper) *cobra.Command {
	var storageClassName string
	var volumeSnapshotClassName string
	var useSnapshot bool
	var testNamespace string
	var containerDiskUrl string
	var dataVolumeSize string
	var iterations int
	var clonesPerIteration int
	var metricsProfiles []string
	var volumeAccessMode string
	var rc int
	cmd := &cobra.Command{
		Use:          dvCloneTestName,
		Short:        "Runs dv-clone workload",
		SilenceUsage: true,
		PreRun: func(cmd *cobra.Command, args []string) {
			if _, ok := accessModeTranslator[volumeAccessMode]; !ok {
				log.Fatalf("Unsupported access mode - %s", volumeAccessMode)
			}

			storageClassName, volumeSnapshotClassName = getStorageAndSnapshotClasses(storageClassName, useSnapshot, cmd.Flags().Lookup("use-snapshot").Changed)

			if cmd.Flags().Lookup("container-disk").Changed && !cmd.Flags().Lookup("datavolume-size").Changed {
				log.Warnf("--container-disk was set without setting --datavolume-size. Make sure the default size [%v] is sufficient", dataVolumeSize)
			}
		},
		Run: func(cmd *cobra.Command, args []string) {

			log.Infof("All resources will be create in the namespace [%s]", testNamespace)
			log.Infof("Using [%s] as the container disk image for the base DataVolume", containerDiskUrl)
			log.Infof("DataVolume size set to [%v]", dataVolumeSize)
			log.Infof("Clone DataVolumes will be created in [%v] iterations of [%v] each", iterations, clonesPerIteration)

			AdditionalVars["storageClassName"] = storageClassName
			AdditionalVars["volumeSnapshotClassName"] = volumeSnapshotClassName
			AdditionalVars["testNamespace"] = testNamespace
			AdditionalVars["containerDiskUrl"] = containerDiskUrl
			AdditionalVars["dataVolumeSize"] = dataVolumeSize
			AdditionalVars["accessMode"] = accessModeTranslator[volumeAccessMode]
			AdditionalVars["iterations"] = iterations
			AdditionalVars["clonesPerIteration"] = clonesPerIteration

			setMetrics(cmd, metricsProfiles)
			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().StringVar(&storageClassName, "storage-class", "", "Name of the Storage Class to test")
	cmd.Flags().BoolVar(&useSnapshot, "use-snapshot", true, "Clone from snapshot")
	cmd.Flags().IntVar(&iterations, "iterations", 2, "Number of iterations to create DataVolume clones . The total number of DataVolumes is iterations*iteration-clones")
	cmd.Flags().IntVar(&clonesPerIteration, "iteration-clones", 10, "How many DataVolumes to create per iteration. The total number of DataVolumes is iterations*iteration-clones")
	cmd.Flags().StringVarP(&testNamespace, "namespace", "n", dvCloneTestName, "Name for the namespace to run the test in")
	cmd.Flags().StringVar(&volumeAccessMode, "access-mode", "RWX", "Access mode for the created volumes - RO, RWO, RWX")
	cmd.Flags().StringVar(&containerDiskUrl, "container-disk", dvCloneDefaultContainerDiskUrl, "URL of the container disk to load into the volume")
	cmd.Flags().StringVar(&dataVolumeSize, "datavolume-size", dvCloneDefaultDataVolumeSize, "Size of the DataVolume to create")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
