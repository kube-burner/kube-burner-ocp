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
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	"github.com/praserx/ipconv"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// get egress IP cidr, node IPs from worker node annotations
func getEgressIPCidrNodeIPs() ([]string, string) {
	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, _ := kubeClientProvider.ClientSet(0, 0)
	workers, err := clientSet.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Errorf("Error retrieving workers: %v", err)
		os.Exit(1)
	}

	nodeIPs := []string{}
	var egressIPCidr string
	for _, worker := range workers.Items {
		nodeIPs = append(nodeIPs, worker.Status.Addresses[0].Address)
		// Add gateway ip to nodeIPs to get excluded while creating egress ip list
		gwconfig, exist := worker.ObjectMeta.Annotations["k8s.ovn.org/l3-gateway-config"]
		if exist {
			var item map[string]interface{}
			json.Unmarshal([]byte(gwconfig), &item)
			defaultgw := item["default"].(map[string]interface{})
			nodeIPs = append(nodeIPs, defaultgw["next-hop"].(string))
		}
		// For cloud based OCP deployedments, egress IP cidr is added as part of cloud.network.openshift.io/egress-ipconfig annotation
		// For baremetal, read the cidr from k8s.ovn.org/node-primary-ifaddr
		if egressIPCidr == "" {
			eipconfig, exist := worker.ObjectMeta.Annotations["cloud.network.openshift.io/egress-ipconfig"]
			if exist {
				var items []map[string]interface{}
				json.Unmarshal([]byte(eipconfig), &items)
				ifaddr := items[0]["ifaddr"].(map[string]interface{})
				egressIPCidr = ifaddr["ipv4"].(string)
			} else {
				nodeAddr, exist := worker.ObjectMeta.Annotations["k8s.ovn.org/node-primary-ifaddr"]
				if exist {
					var ifaddr map[string]interface{}
					json.Unmarshal([]byte(nodeAddr), &ifaddr)
					egressIPCidr = ifaddr["ipv4"].(string)
				}
			}
		}
	}
	return nodeIPs, egressIPCidr
}

// This function returns first usable address from the cidr
// for example, if cidr is 10.0.132.49/19, first usable address is 10.0.128.1
func getFirstUsableAddr(cidr string) uint32 {
	// Parse the IP address and subnet mask
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		fmt.Println("Error parsing CIDR notation:", err)
		os.Exit(1)
	}

	// Get the network address by performing a bitwise AND
	ipBytes := ip.To4()
	networkBytes := make([]byte, 4)
	for i := 0; i < 4; i++ {
		networkBytes[i] = ipBytes[i] & ipNet.Mask[i]
	}

	// Calculate the first usable IP address by skipping first 4 addresses.
	// For example, OVN didn't assign eip to node when eip was in between 10.0.0.0 and 10.0.0.3 for cidr 10.0.0.0/19
	firstUsableIP := make(net.IP, len(networkBytes))
	copy(firstUsableIP, networkBytes)
	firstUsableIP[3] += 4 // Increment the last byte by 1 for the first usable IP address

	// Output the network address and the first usable IP address in CIDR notation
	baseAddrInt, err := ipconv.IPv4ToInt(firstUsableIP)
	if err != nil {
		log.Fatal("Error converting IP to int: ", err)
	}
	return baseAddrInt
}

// egress IPs and node IPs will be in same cidr. So we need to exclude node IPs from CIDR to generate list of available egress IPs.
func generateEgressIPs(numJobIterations int, addressesPerIteration int, externalServerIP string) {

	nodeIPs, egressIPCidr := getEgressIPCidrNodeIPs()
	// Add external server ip to nodeIPs to get excluded while creating egress ip list
	nodeIPs = append(nodeIPs, externalServerIP)
	baseAddrInt := getFirstUsableAddr(egressIPCidr)
	// list to host available egress IPs
	addrSlice := make([]string, 0, (numJobIterations * addressesPerIteration))

	// map to store nodeIPs
	nodeMap := make(map[uint32]bool)
	for _, nodeip := range nodeIPs {
		nodeipuint32, err := ipconv.IPv4ToInt(net.ParseIP(nodeip))
		if err != nil {
			log.Fatal("Error: ", err)
		}
		nodeMap[nodeipuint32] = true
	}

	// Generate ip addresses from CIDR by excluding nodeIPs
	// Extra iterations needed in for loop if we come across node IPs while generating egress IP list
	var newAddr uint32
	for i := 0; i < ((numJobIterations * addressesPerIteration) + len(nodeIPs)); i++ {
		newAddr = baseAddrInt + uint32(i)
		if !nodeMap[newAddr] {
			addrSlice = append(addrSlice, ipconv.IntToIPv4(newAddr).String())
		}
		// break if we already got needed egress IPs
		if len(addrSlice) >= (numJobIterations * addressesPerIteration) {
			break
		}
	}

	// combine all addresses to a string and export as an environment variable
	os.Setenv("EIP_ADDRESSES", strings.Join(addrSlice, " "))
}

// NewClusterDensity holds cluster-density workload
func NewEgressIP(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, addressesPerIteration int
	var externalServerIP string
	var podReadyThreshold time.Duration
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		PreRun: func(cmd *cobra.Command, args []string) {
			os.Setenv("JOB_ITERATIONS", fmt.Sprint(iterations))
			os.Setenv("POD_READY_THRESHOLD", fmt.Sprintf("%v", podReadyThreshold))
			os.Setenv("ADDRESSES_PER_ITERATION", fmt.Sprint(addressesPerIteration))
			os.Setenv("EXTERNAL_SERVER_IP", externalServerIP)
			generateEgressIPs(iterations, addressesPerIteration, externalServerIP)
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, "metrics-egressip.yml")
			wh.Run(cmd.Name())
		},
	}
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 2*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().IntVar(&iterations, "iterations", 0, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().StringVar(&externalServerIP, "external-server-ip", "", "External server IP address")
	cmd.Flags().IntVar(&addressesPerIteration, "addresses-per-iteration", 1, fmt.Sprintf("%v iterations", variant))
	cmd.MarkFlagRequired("iterations")
	cmd.MarkFlagRequired("external-server-ip")
	return cmd
}
