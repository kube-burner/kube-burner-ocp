// Copyright 2022 The Kube-burner Authors.
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
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	"github.com/openshift/client-go/config/clientset/versioned"
	"github.com/praserx/ipconv"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)


func generateEgressIPs(numJobIterations int, addressesPerIteration int, egressCIDR string, nodeips []string, excludeAddresses string) {

	if excludeAddresses != "" {
		nodeips = append(nodeips, strings.Split(excludeAddresses, " ")...)
	}
	addrSlice := make([]string, 0, (numJobIterations * addressesPerIteration))
        baseAddr, _, err := net.ParseCIDR(egressCIDR)
	if err != nil {
                 log.Fatal("Error: ", err)
	 }
        baseAddrInt, err := ipconv.IPv4ToInt(baseAddr)
	if err != nil {
                 log.Fatal("Error: ", err)
	 }

	// map to store nodeips
	nodeMap := make(map[uint32]bool)
	for _, nodeip := range nodeips {
		nodeipuint32, err := ipconv.IPv4ToInt(net.ParseIP(nodeip))
		if err != nil {
                        log.Fatal("Error: ", err)
	        }
		nodeMap[nodeipuint32] = true
	}

	// Generate ip addresses from CIDR by excluding nodeips
	var newAddr uint32
	for i := 0; i < ((numJobIterations * addressesPerIteration) + len(nodeips) ); i++ {
		newAddr = baseAddrInt + uint32(i)
		if !nodeMap[newAddr] {
			addrSlice = append(addrSlice, ipconv.IntToIPv4(newAddr).String())
		}
	}

	// Export environment variables for kube-burner
	os.Setenv("JOB_ITERATIONS", fmt.Sprint(numJobIterations))
	os.Setenv("ADDRESSES_PER_ITERATION", fmt.Sprint(addressesPerIteration))

	// combine all addresses to a string and export as an environment variable
	os.Setenv("EIP_ADDRESSES", strings.Join(addrSlice, " "))
}


// NewClusterDensity holds cluster-density workload
func NewEgressIP(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, churnPercent, churnCycles, addressesPerIteration int
	var churn, svcLatency bool
	var churnDelay, churnDuration time.Duration
	var churnDeletionStrategy, excludeAddresses string
	var podReadyThreshold time.Duration
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		PreRun: func(cmd *cobra.Command, args []string) {
			clientSet, restConfig, err := config.GetClientSet(0, 0)
			if err != nil {
				log.Fatalf("Error creating clientSet: %s", err)
			}
			openshiftClientset, err := versioned.NewForConfig(restConfig)
			if err != nil {
				log.Fatalf("Error creating OpenShift clientset: %v", err)
			}
			if !ClusterHealthyOcp(clientSet, openshiftClientset) {
				os.Exit(1)
			}
			wh.Metadata.Benchmark = cmd.Name()
			os.Setenv("JOB_ITERATIONS", fmt.Sprint(iterations))
			os.Setenv("CHURN", fmt.Sprint(churn))
			os.Setenv("CHURN_CYCLES", fmt.Sprintf("%v", churnCycles))
			os.Setenv("CHURN_DURATION", fmt.Sprintf("%v", churnDuration))
			os.Setenv("CHURN_DELAY", fmt.Sprintf("%v", churnDelay))
			os.Setenv("CHURN_PERCENT", fmt.Sprint(churnPercent))
			os.Setenv("CHURN_DELETION_STRATEGY", churnDeletionStrategy)
			os.Setenv("POD_READY_THRESHOLD", fmt.Sprintf("%v", podReadyThreshold))
			os.Setenv("SVC_LATENCY", strconv.FormatBool(svcLatency))
			ingressDomain, err := wh.MetadataAgent.GetDefaultIngressDomain()
			if err != nil {
				log.Fatal("Error obtaining default ingress domain: ", err.Error())
			}
			os.Setenv("INGRESS_DOMAIN", ingressDomain)
			os.Setenv("ADDRESSES_PER_ITERATION", fmt.Sprint(addressesPerIteration))
			nodeIPs, egressCIDR := ClusterEgressIPInfo(clientSet, openshiftClientset)
			os.Setenv("EGRESSIP_CIDR", egressCIDR)
			fmt.Printf("ANIL EGRESSIP_CIDR %s \n", egressCIDR)
			fmt.Printf("ANIL nodeIPs %s \n", nodeIPs)
			generateEgressIPs(iterations, addressesPerIteration, egressCIDR, nodeIPs, excludeAddresses)
			fmt.Printf("ANIL EIP_ADDRESSES %s", os.Getenv("EIP_ADDRESSES"))
		},
		Run: func(cmd *cobra.Command, args []string) {
			wh.Run(cmd.Name(), getMetrics(cmd, "metrics-aggregated.yml"), alertsProfiles)
		},
	}
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 2*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().IntVar(&iterations, "iterations", 0, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().BoolVar(&churn, "churn", true, "Enable churning")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 1*time.Hour, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().StringVar(&churnDeletionStrategy, "churn-deletion-strategy", "default", "Churn deletion strategy to use")
	cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	cmd.Flags().IntVar(&addressesPerIteration, "addresses-per-iteration", 1, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().StringVar(&excludeAddresses, "exclude-addresses", "", "List of addresses to exclude for EIP. Example '10.0.0.0 10.0.0.1'")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
