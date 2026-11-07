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

package measurements

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

// podAnnotation decodes addresses from the OVN pod-networks annotation.
type podAnnotation struct {
	IPs      []string   `json:"ip_addresses"`
	MAC      string     `json:"mac_address"`
	Gateways []string   `json:"gateway_ips,omitempty"`
	Routes   []podRoute `json:"routes,omitempty"`

	IP      string `json:"ip_address,omitempty"`
	Gateway string `json:"gateway_ip,omitempty"`

	TunnelID int    `json:"tunnel_id,omitempty"`
	Role     string `json:"role,omitempty"`
}

// podRoute describes a route in the OVN pod-networks annotation.
type podRoute struct {
	Dest    string `json:"dest"`
	NextHop string `json:"nextHop"`
}

// cudnPods maps a discovered subnet to its CUDN name and pod addresses.
type cudnPods struct {
	cudn string
	pods []string
}

// cudnPodAddress identifies a pod address and the CUDN subnet it belongs to.
type cudnPodAddress struct {
	cudn   string
	subnet string
	ip     string
}

// discoverCUDNPods discovers CUDN subnets and pod addresses for both scenarios using
// OVN annotations on pods belonging to this benchmark.
func discoverCUDNPods(ctx context.Context, client corev1.CoreV1Interface, uuid string) (map[string]cudnPods, error) {
	cudnSubnet := make(map[string]cudnPods)
	listOptions := metav1.ListOptions{LabelSelector: fmt.Sprintf("%s=%s", config.KubeBurnerLabelUUID, uuid)}
	nsList, err := client.Namespaces().List(ctx, listOptions)
	if err != nil {
		log.Errorf("Error listing namespaces: %v", err)
		return cudnSubnet, err
	}
	for _, ns := range nsList.Items {
		podList, err := client.Pods(ns.Name).List(ctx, listOptions)
		if err != nil {
			log.Errorf("Error listing pods in namespace %s: %v", ns.Name, err)
			return cudnSubnet, err
		}
		for _, pod := range podList.Items {
			for _, address := range parseCUDNPodAddresses(pod.Annotations) {
				entry, exists := cudnSubnet[address.subnet]
				if !exists {
					entry.cudn = address.cudn
				}
				entry.pods = append(entry.pods, address.ip)
				cudnSubnet[address.subnet] = entry
			}
		}
	}
	return cudnSubnet, nil
}

// parseCUDNPodAddresses decodes non-default networks from the OVN annotation.
// Missing annotations and malformed entries are skipped, with errors logged.
func parseCUDNPodAddresses(annotations map[string]string) []cudnPodAddress {
	ovnAnnotation, ok := annotations["k8s.ovn.org/pod-networks"]
	if !ok {
		return nil
	}
	podNetworks := make(map[string]podAnnotation)
	if err := json.Unmarshal([]byte(ovnAnnotation), &podNetworks); err != nil {
		log.Errorf("failed to unmarshal ovn pod annotation  %v", err)
		return nil
	}

	var addresses []cudnPodAddress
	for networkName, network := range podNetworks {
		if networkName == "default" {
			continue
		}
		parts := strings.Split(networkName, "/")
		if len(parts) != 2 {
			log.Debugf("Invalid input format")
			continue
		}
		ip, subnet, err := net.ParseCIDR(network.IP)
		if err != nil {
			log.Debugf("Unable to get CIDR for IP")
			continue
		}
		addresses = append(addresses, cudnPodAddress{
			cudn:   parts[1],
			subnet: subnet.String(),
			ip:     ip.String(),
		})
	}
	return addresses
}
