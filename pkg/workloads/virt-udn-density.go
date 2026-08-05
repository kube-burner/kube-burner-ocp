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
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
)

// Returns virt-density workload
func NewVirtUDNDensity(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, vmsPerNode, vlanStart, vmsPerUDN int
	var vmiRunningThreshold, pprofInterval time.Duration
	var metricsProfiles []string
	var churnPercent, churnCycles int
	var l3, localnet, enableIPAM, pprof bool
	var churnDelay, churnDuration time.Duration
	var deletionStrategy, vmImage, bindingMethod, churnMode, physicalNetworkName string
	var rc int
	cmd := &cobra.Command{
		Use:          variant,
		Short:        fmt.Sprintf("Runs %v workload", variant),
		SilenceUsage: true,
		Run: func(cmd *cobra.Command, args []string) {
			if bindingMethod != "passt" && bindingMethod != "l2bridge" {
				fmt.Println("Invalid value for --binding-method. Allowed values are 'passt' or 'l2bridge'.")
				os.Exit(1)
			}
			if iterations < 1 {
				fmt.Println("--iterations must be >= 1.")
				os.Exit(1)
			}
			if localnet && l3 {
				fmt.Println("Cannot enable both --localnet and --layer3. Choose one topology.")
				os.Exit(1)
			}
			if vlanStart != 0 && !localnet {
				fmt.Println("--vlan-start can only be used with --localnet topology.")
				os.Exit(1)
			}
			if vlanStart < 0 || vlanStart > 4094 {
				fmt.Println("--vlan-start must be between 0 and 4094 (0 means no VLAN).")
				os.Exit(1)
			}
			if vlanStart > 0 && vlanStart+iterations-1 > 4094 {
				fmt.Printf("--vlan-start %d with --iterations %d would exceed max VLAN ID 4094 (max would be %d).\n", vlanStart, iterations, vlanStart+iterations-1)
				os.Exit(1)
			}
			if enableIPAM && !localnet {
				fmt.Println("--enable-ipam can only be used with --localnet topology.")
				os.Exit(1)
			}
			if physicalNetworkName != "" && !localnet {
				fmt.Println("--physical-network can only be used with --localnet topology.")
				os.Exit(1)
			}

			// Generate SSH key pair and save to /tmp
			if err := generateSSHKey(); err != nil {
				log.Fatalf("Failed to generate SSH key: %v", err)
			}

			setMetrics(cmd, metricsProfiles)

			var clientVMsPerUdn int
			if vmsPerUDN > 0 {
				// Use the simple fixed count per UDN
				clientVMsPerUdn = vmsPerUDN - 1 // -1 for the server VM
				if clientVMsPerUdn < 0 {
					clientVMsPerUdn = 0
				}
				log.Infof("Using fixed count: %d VMs per UDN (1 server + %d clients)", vmsPerUDN, clientVMsPerUdn)
			} else {
				// Use the original calculation based on cluster size
				totalVMs := clusterMetadata.WorkerNodesCount * vmsPerNode
				clientVMsPerUdn = totalVMs/iterations - 1 // -1 because there is always one server vm per udn
				if clientVMsPerUdn < 1 {
					log.Warn("Nb of total client VMs to deploy is less than the number of UDNs, only the server VM will be deployed")
					clientVMsPerUdn = 0
				}
			}

			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["CHURN_MODE"] = churnMode
			AdditionalVars["DELETION_STRATEGY"] = deletionStrategy
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["VMS_PER_ITERATION"] = clientVMsPerUdn
			AdditionalVars["VMI_RUNNING_THRESHOLD"] = vmiRunningThreshold
			AdditionalVars["VM_IMAGE"] = vmImage
			AdditionalVars["UDN_BINDING_METHOD"] = bindingMethod
			AdditionalVars["ENABLE_LAYER_3"] = l3
			AdditionalVars["ENABLE_LOCALNET"] = localnet
			AdditionalVars["VLAN_START"] = vlanStart
			AdditionalVars["ENABLE_IPAM"] = enableIPAM
			AdditionalVars["PHYSICAL_NETWORK_NAME"] = physicalNetworkName
			AdditionalVars["PPROF"] = pprof
			AdditionalVars["PPROF_INTERVAL"] = pprofInterval.String()

			// Read SSH public key and add to template variables
			sshPubKey, err := os.ReadFile("/tmp/kube-burner-vm-key.pub")
			if err != nil {
				log.Fatalf("Failed to read SSH public key: %v", err)
			}
			AdditionalVars["SSH_PUBLIC_KEY"] = string(sshPubKey)
			if localnet {
				logMsg := "Localnet topology is enabled"
				if physicalNetworkName != "" {
					logMsg = fmt.Sprintf("%s on physical network '%s'", logMsg, physicalNetworkName)
				}
				if vlanStart != 0 {
					logMsg = fmt.Sprintf("%s with VLANs starting at %d", logMsg, vlanStart)
				}
				if enableIPAM {
					logMsg = fmt.Sprintf("%s with IPAM enabled", logMsg)
				}
				log.Info(logMsg)
				AddVirtMetadata(wh, vmImage, "localnet", bindingMethod)
			} else if l3 {
				log.Info("Layer 3 is enabled")
				AddVirtMetadata(wh, vmImage, "layer3", bindingMethod)
			} else {
				log.Info("Layer 2 is enabled")
				AddVirtMetadata(wh, vmImage, "layer2", bindingMethod)
			}
			rc = RunWorkload(cmd, wh, variant+".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().BoolVar(&l3, "layer3", false, "Enable Layer3 UDN instead of Layer2, default: false - layer2 enabled")
	cmd.Flags().BoolVar(&localnet, "localnet", false, "Enable Localnet UDN topology, default: false - layer2 enabled")
	cmd.Flags().StringVar(&physicalNetworkName, "physical-network", "", "Physical network name for localnet topology. Only valid with --localnet")
	cmd.Flags().IntVar(&vlanStart, "vlan-start", 0, "Starting VLAN ID for localnet CUDNs (0 = no VLAN, valid range: 1-4094). Each iteration increments the VLAN ID. Only valid with --localnet")
	cmd.Flags().BoolVar(&enableIPAM, "enable-ipam", false, "Enable IPAM for localnet topology. Only valid with --localnet")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().StringVar(&vmImage, "vm-image", "quay.io/openshift-cnv/qe-cnv-tests-fedora:40", "Vm Image to be deployed")
	cmd.Flags().StringVar(&bindingMethod, "binding-method", "l2bridge", "Binding method for the VM UDN network interface - acceptable values: 'l2bridge' | 'passt'")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 0, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&churnMode, "churn-mode", string(config.ChurnNamespaces), "Either namespaces, to churn entire namespaces or objects, to churn individual objects")
	cmd.Flags().StringVar(&deletionStrategy, "deletion-strategy", config.DefaultDeletionStrategy, "GC deletion mode, default deletes entire namespaces and gvr deletes objects within namespaces before deleting the parent namespace")
	cmd.Flags().IntVar(&iterations, "iterations", 1, "Job iterations, (One UDN will be created per iteration)")
	cmd.Flags().IntVar(&vmsPerNode, "vms-per-node", 50, "VMs per node")
	cmd.Flags().IntVar(&vmsPerUDN, "vms-per-udn", 0, "Fixed number of VMs per UDN (includes 1 server + N clients). When set, overrides the vms-per-node calculation. Use 0 to disable (default: auto-calculate based on cluster size)")
	cmd.Flags().DurationVar(&vmiRunningThreshold, "vmi-ready-threshold", 0, "VMI ready timeout threshold")
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Enable pprof collection")
	cmd.Flags().DurationVar(&pprofInterval, "pprof-interval", 0, "Interval between pprof collections")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	if variant == "virt-cudn-density" {
		cmd.Annotations = map[string]string{"configDir": "virt-udn-density"}
	}
	return cmd
}

// generateSSHKey generates an SSH key pair and saves it to /tmp
func generateSSHKey() error {
	privateKeyPath := "/tmp/kube-burner-vm-key"
	publicKeyPath := "/tmp/kube-burner-vm-key.pub"

	// Check if both key files already exist
	_, privErr := os.Stat(privateKeyPath)
	_, pubErr := os.Stat(publicKeyPath)
	if privErr == nil && pubErr == nil {
		log.Infof("SSH key pair already exists at %s", privateKeyPath)
		return nil
	}

	log.Info("Generating SSH key pair for VM access")

	// Generate RSA key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// Encode private key to PEM format
	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}

	// Write private key to file
	privateKeyFile, err := os.OpenFile(privateKeyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create private key file: %w", err)
	}
	defer privateKeyFile.Close()

	if err := pem.Encode(privateKeyFile, privateKeyPEM); err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}

	// Generate public key
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		return fmt.Errorf("failed to generate public key: %w", err)
	}

	// Write public key to file
	publicKeyBytes := ssh.MarshalAuthorizedKey(publicKey)
	if err := os.WriteFile(publicKeyPath, publicKeyBytes, 0644); err != nil {
		return fmt.Errorf("failed to write public key: %w", err)
	}

	log.Infof("SSH key pair generated successfully:")
	log.Infof("  Private key: %s", privateKeyPath)
	log.Infof("  Public key: %s", publicKeyPath)
	log.Infof("  Use: ssh -i %s fedora@<vm-ip>", privateKeyPath)

	return nil
}
