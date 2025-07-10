// Copyright 2023 The Kube-burner Authors.
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
	"regexp"
	"strings"

	"github.com/kube-burner/kube-burner/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var dynamicStorageProvisioners = map[string]string{
	"aws":        "kubernetes.io/aws-ebs",
	"cinder":     "kubernetes.io/cinder",
	"azure-disk": "kubernetes.io/azure-disk",
	"azure-file": "kubernetes.io/azure-file",
	"gce":        "kubernetes.io/gce-pd",
	"ibm":        "powervs.csi.ibm.com",
	"vsphere":    "kubernetes.io/vsphere-volume",
	"oci":        "blockvolume.csi.oraclecloud.com",
}

// NewPVCDensity holds pvc-density workload
func NewPVCDensity(wh *workloads.WorkloadHelper) *cobra.Command {

	var iterations int
	var storageProvisioners, metricsProfiles []string
	var claimSize string
	var containerImage string
	var rc int
	provisioner := "aws"

	cmd := &cobra.Command{
		Use:          "pvc-density",
		Short:        "Runs pvc-density workload",
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			for key := range dynamicStorageProvisioners {
				storageProvisioners = append(storageProvisioners, key)
			}
			re := regexp.MustCompile(`(?sm)^(cinder|azure\-disk|azure\-file|gce|ibm|vsphere|aws|oci)$`)
			if !re.MatchString(provisioner) {
				log.Fatal(fmt.Errorf("%s does not match one of %s", provisioner, storageProvisioners))
			}

			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["CONTAINER_IMAGE"] = containerImage
			AdditionalVars["CLAIM_SIZE"] = claimSize
			AdditionalVars["STORAGE_PROVISIONER"] = dynamicStorageProvisioners[provisioner]

			setMetrics(cmd, metricsProfiles)
			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, nil)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}

	cmd.Flags().IntVar(&iterations, "iterations", 0, fmt.Sprintf("%v iterations", iterations))
	cmd.Flags().StringVar(&provisioner, "provisioner", provisioner, fmt.Sprintf("[%s]", strings.Join(storageProvisioners, " ")))
	cmd.Flags().StringVar(&claimSize, "claim-size", "256Mi", "claim-size=256Mi")
	cmd.Flags().StringVar(&containerImage, "container-image", "gcr.io/google_containers/pause:3.1", "Container image")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	return cmd
}
