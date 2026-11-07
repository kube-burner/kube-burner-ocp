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

// getPods discovers CUDN subnets and pod addresses for both scenarios using
// OVN annotations on pods belonging to this benchmark.
func getPods(ctx context.Context, client corev1.CoreV1Interface, uuid string) (map[string]cudnPods, error) {
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
			podNetworks := make(map[string]podAnnotation)
			ovnAnnotation, ok := pod.Annotations["k8s.ovn.org/pod-networks"]
			if ok {
				if err := json.Unmarshal([]byte(ovnAnnotation), &podNetworks); err != nil {
					log.Errorf("failed to unmarshal ovn pod annotation  %v", err)
					continue
				}
				for pnet, val := range podNetworks {
					if pnet != "default" {
						var udn string
						parts := strings.Split(pnet, "/")
						if len(parts) == 2 {
							_, udn = parts[0], parts[1]
						} else {
							log.Debugf("Invalid input format")
							continue
						}
						ipAddr, subnet, err := net.ParseCIDR(val.IP)
						if err != nil {
							log.Debugf("Unable to get CIDR for IP")
							continue
						}
						subnetString := subnet.String()
						ipAddrString := ipAddr.String()
						cudnpods, exists := cudnSubnet[subnetString]
						if exists {
							cudnpods.pods = append(cudnpods.pods, ipAddrString)
							cudnSubnet[subnetString] = cudnpods
						} else {
							cudnSubnet[subnetString] = cudnPods{
								cudn: udn,
								pods: []string{ipAddrString},
							}
						}
					}
				}
			}
		}
	}
	return cudnSubnet, nil
}
