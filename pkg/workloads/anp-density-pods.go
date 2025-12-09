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
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

type PodInfo struct {
	Name string
	IP   string
}

// AdminNetworkPolicy GVR (Group Version Resource)
var anpGVR = schema.GroupVersionResource{
	Group:    "policy.networking.k8s.io",
	Version:  "v1alpha1",
	Resource: "adminnetworkpolicies",
}

func getNamespacesByPrefix(prefix string) ([]string, error) {
	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, _ := kubeClientProvider.ClientSet(0, 0)
	nsList, err := clientSet.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})

	if err != nil {
		return nil, err
	}
	var ns []string
	for _, item := range nsList.Items {
		if strings.HasPrefix(item.Name, prefix) {
			ns = append(ns, item.Name)
		}
	}
	return ns, nil
}

func getPodsByNamespaceAndPattern(namespace, pattern string) ([]PodInfo, error) {
	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, _ := kubeClientProvider.ClientSet(0, 0)
	podList, err := clientSet.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{})

	if err != nil {
		return nil, err
	}
	var pods []PodInfo
	for _, pod := range podList.Items {
		if strings.Contains(pod.Name, pattern) && pod.Status.PodIP != "" {
			pods = append(pods, PodInfo{Name: pod.Name, IP: pod.Status.PodIP})
		}
	}
	return pods, nil
}

// Apply AdminNetworkPolicy using Dynamic Client
func applyWithDynamicClient(config *rest.Config, yamlString string) error {
	// Create dynamic client
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %v", err)
	}

	// Parse YAML to unstructured object
	obj := &unstructured.Unstructured{}
	dec := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	_, _, err = dec.Decode([]byte(yamlString), nil, obj)
	if err != nil {
		return fmt.Errorf("failed to decode YAML: %v", err)
	}

	// Get the resource interface for AdminNetworkPolicy
	anpClient := dynamicClient.Resource(anpGVR)
	ctx := context.TODO()
	name := obj.GetName()

	existing, err := anpClient.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		// Resource doesn't exist, create it
		_, err = anpClient.Create(ctx, obj, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create AdminNetworkPolicy: %v", err)
		}
		log.Info("Created AdminNetworkPolicy/", name)
	} else {
		// Resource exists, update it
		obj.SetResourceVersion(existing.GetResourceVersion())
		_, err = anpClient.Update(ctx, obj, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update AdminNetworkPolicy: %v", err)
		}
		log.Info("Applied AdminNetworkPolicy/", name)
	}
	return nil
}

func verifyAdminNetworkPolicies(config *rest.Config, expectedANPs int) error {
	log.Info("Verifying created objects")
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %v", err)
	}

	anpList, err := dynamicClient.Resource(anpGVR).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list adminnetworkpolicies: %v", err)
	}

	for _, item := range anpList.Items {
		status, found, err := unstructured.NestedMap(item.Object, "status")
		resourceName := item.GetName()
		if err != nil || !found {
			log.Debug("status not found for adminnetworkpolicies", item.GetName())
			continue
		} else {
			// For status.conditions pattern
			conditions := status["conditions"].([]interface{})
			reason := conditions[0].(map[string]interface{})["reason"].(string)
			log.Debug("adminnetworkpolicies: ", resourceName, " ", reason)
		}
	}
	log.Info("adminnetworkpolicies found: ", len(anpList.Items), " Expected: ", expectedANPs)
	if len(anpList.Items) == 0 || len(anpList.Items) != expectedANPs {
		return fmt.Errorf("No adminnetworkpolicies found or mismatch expect number of ANPs.: %v", err)
	}
	return nil
}

func generateCidrSelectorAnpMultiPolicyWithMultiRulesMultiIPsByTenant(
	sourceNsPrefix, targetNsPrefix string,
	targetNsAllowPod string,
	targetNsAllowPort string,
	targetNsDenyPod string,
	targetNsDenyPort string,
	totalNsByTenant int,
	totalIpBlockNumByRule int,
) error {
	if sourceNsPrefix == "" || targetNsPrefix == "" {
		return fmt.Errorf("please specify targetNsPrefix or sourceNsPrefix")
	}

	sourceNsList, err := getNamespacesByPrefix(sourceNsPrefix)
	if err != nil || len(sourceNsList) == 0 {
		return fmt.Errorf("No source namespace with prefix %s was found", sourceNsPrefix)
	}

	// For simplicity, only use the first source ns
	tenantID := 1
	priority := 1
	newTenant := true
	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, restConfig := kubeClientProvider.ClientSet(0, 0)

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %v", err)
	}

	anpList, err := dynamicClient.Resource(anpGVR).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list AdminNetworkPolicies: %v", err)
	}

	currentANPs := len(anpList.Items)
	// For each tenant, generate a YAML
	for nsIdx, sns := range sourceNsList {
		if nsIdx%totalNsByTenant == 0 && nsIdx != 0 {
			tenantID++
			priority++
			newTenant = true
			if priority > 99 {
				priority = 1
			}
		}
		// Label the namespace
		patchOps := []byte(`[{"op": "add", "path": "/metadata/labels/customer_tenant", "value": "tenant` + fmt.Sprintf("%d", tenantID) + `"}]`)

		_, err = clientSet.CoreV1().Namespaces().Patch(context.TODO(), sns, types.JSONPatchType, patchOps, metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("failed to patch namespace %s: %v", sns, err)
		}

		// Get pods in target namespace
		allowPods, _ := getPodsByNamespaceAndPattern(targetNsPrefix, targetNsAllowPod)
		denyPods, _ := getPodsByNamespaceAndPattern(targetNsPrefix, targetNsDenyPod)

		var yaml bytes.Buffer
		yaml.WriteString(fmt.Sprintf(`apiVersion: policy.networking.k8s.io/v1alpha1
kind: AdminNetworkPolicy
metadata:
  name: anp-cidr-selector-policy-rules-%s-to-%s-network-tenant%d-p%d
spec:
  priority: %d
  subject:
    namespaces:
      matchLabels:
        customer_tenant: tenant%d
  ingress:
  - name: "all-ingress-from-same-tenant"  
    action: Allow   # Allows connection 
    from:
    - namespaces:
        matchLabels:
          customer_tenat: tenant%d
  egress:                           
  - name: "pass-egress-to-cluster-network"
    action: "Pass"
    ports:
      - portNumber:
          port: 9093
          protocol: TCP
      - portNumber:
          port: 9094
          protocol: TCP    
    to:
    - networks:
      - 10.128.0.0/14
`, sourceNsPrefix, targetNsPrefix, tenantID, priority, priority, tenantID, tenantID))

		// Allow rules for allowPods
		appRuleIndex := 1
		for i := 0; i < len(allowPods); i += totalIpBlockNumByRule {
			yaml.WriteString(fmt.Sprintf("  - name: \"allow-egress-to-%s-network-%d\"\n    action: \"Allow\"\n    ports:\n      - portNumber:\n          port: %s\n      - portNumber:\n          port: 8080\n          protocol: TCP\n      - portRange:\n          start: 9201\n          end: 9205\n          protocol: TCP\n    to:\n    - networks:\n", targetNsPrefix, appRuleIndex, targetNsAllowPort))
			for j := i; j < i+totalIpBlockNumByRule && j < len(allowPods); j++ {
				yaml.WriteString(fmt.Sprintf("      - %s/32\n", allowPods[j].IP))
			}
			appRuleIndex++
		}

		// Deny rules for denyPods
		dbRuleIndex := 1
		for i := 0; i < len(denyPods); i += totalIpBlockNumByRule {
			yaml.WriteString(fmt.Sprintf("  - name: \"deny-egress-to-%s-network-%d\"\n    action: \"Deny\"\n    ports:\n      - portNumber:\n          port: %s\n      - portNumber:\n          port: 5432\n          protocol: TCP\n      - portNumber:\n          port: 60000\n          protocol: TCP\n      - portNumber:\n          port: 9099\n          protocol: TCP\n      - portNumber:\n          port: 9393\n          protocol: TCP\n    to:\n    - networks:\n", targetNsPrefix, dbRuleIndex, targetNsDenyPort))
			for j := i; j < i+totalIpBlockNumByRule && j < len(denyPods); j++ {
				yaml.WriteString(fmt.Sprintf("      - %s/32\n", denyPods[j].IP))
			}
			dbRuleIndex++
		}

		// Optionally, print YAML to stdout for debugging
		if newTenant {
			newTenant = false
			// Using Dynamic Client to apply the YAML
			if err := applyWithDynamicClient(restConfig, yaml.String()); err != nil {
				log.Fatalf("Error applying with dynamic client: %v", err)
			}
		}
	}

	verifyAdminNetworkPolicies(restConfig, tenantID+currentANPs)
	return nil
}

// NewUDNDensityPods holds udn-density-pods workload
func NewANPDensityPods(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var churnPercent, churnCycles, iterations int
	var churn, svcLatency, simple, pprof bool
	var jobPause time.Duration
	var churnDelay, churnDuration, podReadyThreshold time.Duration
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)

			if churn {
				log.Info("Churn is enabled, there will not be a pause after UDN creation")
			}

			AdditionalVars["PPROF"] = pprof
			AdditionalVars["SIMPLE"] = simple
			AdditionalVars["CHURN"] = churn
			AdditionalVars["CHURN_CYCLES"] = churnCycles
			AdditionalVars["CHURN_DURATION"] = churnDuration
			AdditionalVars["CHURN_DELAY"] = churnDelay
			AdditionalVars["CHURN_PERCENT"] = churnPercent
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["POD_READY_THRESHOLD"] = podReadyThreshold
			AdditionalVars["SVC_LATENCY"] = svcLatency

			rc = wh.RunWithAdditionalVars(cmd.Name()+".yml", AdditionalVars, nil)

			sourceNsPrefix := "anp-cidr"
			targetNsPrefix := "openshift-monitoring"
			targetNsAllowPod := "node-exporter"
			targetNsAllowPort := "9100"
			targetNsDenyPod := "prometheus-k8s"
			targetNsDenyPort := "9091"
			totalNsByTenant := 3
			totalIpBlockNumByRule := 3

			err := generateCidrSelectorAnpMultiPolicyWithMultiRulesMultiIPsByTenant(
				sourceNsPrefix,
				targetNsPrefix,
				targetNsAllowPod,
				targetNsAllowPort,
				targetNsDenyPod,
				targetNsDenyPort,
				totalNsByTenant,
				totalIpBlockNumByRule,
			)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
			}
			log.Infof("👋 kube-burner run completed with rc %d for UUID %s", rc, wh.UUID)
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().DurationVar(&jobPause, "job-pause", 0, "Time to pause after finishing the job that creates the UDN")
	cmd.Flags().BoolVar(&pprof, "pprof", false, "Enable pprof collection")
	cmd.Flags().BoolVar(&simple, "simple", false, "only client and server pods to be deployed, no services and networkpolicies")
	cmd.Flags().BoolVar(&churn, "churn", false, "Enable churning")
	cmd.Flags().IntVar(&churnCycles, "churn-cycles", 0, "Churn cycles to execute")
	cmd.Flags().DurationVar(&churnDuration, "churn-duration", 1*time.Hour, "Churn duration")
	cmd.Flags().DurationVar(&churnDelay, "churn-delay", 2*time.Minute, "Time to wait between each churn")
	cmd.Flags().IntVar(&churnPercent, "churn-percent", 10, "Percentage of job iterations that kube-burner will churn each round")
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Iterations")
	cmd.Flags().BoolVar(&svcLatency, "service-latency", false, "Enable service latency measurement")
	cmd.Flags().DurationVar(&podReadyThreshold, "pod-ready-threshold", 1*time.Minute, "Pod ready timeout threshold")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
