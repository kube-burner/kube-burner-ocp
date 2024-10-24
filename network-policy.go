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
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/kube-burner/kube-burner/pkg/config"
	kutil "github.com/kube-burner/kube-burner/pkg/util"
	"github.com/kube-burner/kube-burner/pkg/workloads"
	routev1 "github.com/openshift/api/route/v1"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	openshiftrouteclientset "github.com/openshift/client-go/route/clientset/versioned"
)

var networkPolicyProxyPort int32 = 9002
var networkPolicyProxy = "network-policy-proxy"
var networkPolicyProxyLabel = map[string]string{"app": networkPolicyProxy}
var networkPolicyProxyRouteName string

var networkPolicyProxyPod = &corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name:      networkPolicyProxy,
		Namespace: networkPolicyProxy,
		Labels:    networkPolicyProxyLabel,
	},
	Spec: corev1.PodSpec{
		TerminationGracePeriodSeconds: ptr.To[int64](0),
		Containers: []corev1.Container{
			{
				Image:           "quay.io/cloud-bulldozer/netpolproxy:latest",
				Name:            networkPolicyProxy,
				ImagePullPolicy: corev1.PullAlways,
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To[bool](false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					RunAsNonRoot:             ptr.To[bool](true),
					SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					RunAsUser:                ptr.To[int64](1000),
				},
			},
		},
	},
}

var networkPolicyProxySvc = &corev1.Service{
	ObjectMeta: metav1.ObjectMeta{
		Name:      networkPolicyProxy,
		Namespace: networkPolicyProxy,
		Labels:    networkPolicyProxyLabel,
	},
	Spec: corev1.ServiceSpec{
		Ports: []corev1.ServicePort{
			{
				Protocol:   corev1.ProtocolTCP,
				TargetPort: intstr.Parse(fmt.Sprintf("%d", networkPolicyProxyPort)),
				Port:       80,
				Name:       "http",
			},
		},
		Type:     corev1.ServiceType("ClusterIP"),
		Selector: networkPolicyProxyLabel,
	},
}

var networkPolicyProxyRoute = routev1.Route{
	ObjectMeta: metav1.ObjectMeta{
		Name:   networkPolicyProxy,
		Labels: networkPolicyProxyLabel,
	},
	Spec: routev1.RouteSpec{
		Port: &routev1.RoutePort{TargetPort: intstr.FromString("http")},
		To: routev1.RouteTargetReference{
			Name: networkPolicyProxy,
		},
	},
}

// create proxy pod with route
func deployAssets(uuid string, clientSet kubernetes.Interface, restConfig *rest.Config) error {
	var err error
	orClientSet := openshiftrouteclientset.NewForConfigOrDie(restConfig)
	nsLabels := map[string]string{"kube-burner-uuid": uuid}
	if err = kutil.CreateNamespace(clientSet, networkPolicyProxy, nsLabels, nil); err != nil {
		return err
	}
	if _, err = clientSet.CoreV1().Pods(networkPolicyProxy).Create(context.TODO(), networkPolicyProxyPod, metav1.CreateOptions{}); err != nil {
		if errors.IsAlreadyExists(err) {
			log.Warn(err)
		} else {
			return err
		}
	}
	wait.PollUntilContextCancel(context.TODO(), 100*time.Millisecond, true, func(ctx context.Context) (done bool, err error) {
		pod, err := clientSet.CoreV1().Pods(networkPolicyProxy).Get(context.TODO(), networkPolicyProxy, metav1.GetOptions{})
		if err != nil {
			return true, err
		}
		if pod.Status.Phase != corev1.PodRunning {
			return false, nil
		}
		return true, nil
	})
	_, err = clientSet.CoreV1().Services(networkPolicyProxy).Create(context.TODO(), networkPolicyProxySvc, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	} else {
		r, err := orClientSet.RouteV1().Routes(networkPolicyProxy).Create(context.TODO(), &networkPolicyProxyRoute, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			return err
		}
		networkPolicyProxyRouteName = r.Spec.Host
	}

	return err
}

// NewNetworkPolicy holds network-policy workload
func NewNetworkPolicy(wh *workloads.WorkloadHelper, variant string) *cobra.Command {
	var iterations, podsPerNamespace, netpolPerNamespace, localPods, podSelectors, singlePorts, portRanges, remoteNamespaces, remotePods, cidrs int
	var netpolLatency bool
	var metricsProfiles []string
	var rc int
	var convergenceTimeout, convergencePeriod int

	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, restConfig := kubeClientProvider.ClientSet(0, 0)
	err := deployAssets(wh.Config.UUID, clientSet, restConfig)
	if err != nil {
		log.Fatal("Error: ", err)
		os.Exit(1)
	}
	cmd := &cobra.Command{
		Use:   variant,
		Short: fmt.Sprintf("Runs %v workload", variant),
		PreRun: func(cmd *cobra.Command, args []string) {
			os.Setenv("JOB_ITERATIONS", fmt.Sprint(iterations))
			os.Setenv("PODS_PER_NAMESPACE", fmt.Sprint(podsPerNamespace))
			os.Setenv("NETPOLS_PER_NAMESPACE", fmt.Sprint(netpolPerNamespace))
			os.Setenv("LOCAL_PODS", fmt.Sprint(localPods))
			os.Setenv("POD_SELECTORS", fmt.Sprint(podSelectors))
			os.Setenv("SINGLE_PORTS", fmt.Sprint(singlePorts))
			os.Setenv("PORT_RANGES", fmt.Sprint(portRanges))
			os.Setenv("REMOTE_NAMESPACES", fmt.Sprint(remoteNamespaces))
			os.Setenv("REMOTE_PODS", fmt.Sprint(remotePods))
			os.Setenv("CIDRS", fmt.Sprint(cidrs))
			os.Setenv("NETPOL_LATENCY", strconv.FormatBool(netpolLatency))
			os.Setenv("NETWORK_POLICY_PROXY_ROUTE", networkPolicyProxyRouteName)
			os.Setenv("CONVERGENCE_TIMEOUT", fmt.Sprint(convergenceTimeout))
			os.Setenv("CONVERGENCE_PERIOD", fmt.Sprint(convergencePeriod))
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			rc = wh.Run(cmd.Name())
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			log.Info("Deleting namespace ", networkPolicyProxy)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			labelSelector := fmt.Sprintf("kubernetes.io/metadata.name=%s", networkPolicyProxy)
			kutil.CleanupNamespaces(ctx, clientSet, labelSelector)
			log.Info("👋 Exiting kube-burner ", wh.Config.UUID)
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 10, fmt.Sprintf("%v iterations", variant))
	cmd.Flags().IntVar(&podsPerNamespace, "pods-per-namespace", 10, "Number of pods created in a namespace")
	cmd.Flags().IntVar(&netpolPerNamespace, "netpol-per-namespace", 10, "Number of network policies created in a namespace")
	cmd.Flags().IntVar(&localPods, "local-pods", 2, "Number of pods on the local namespace to receive traffic from remote namespace pods")
	cmd.Flags().IntVar(&podSelectors, "pod-selectors", 1, "Number of pod and namespace selectors to be used in ingress and egress rules")
	cmd.Flags().IntVar(&singlePorts, "single-ports", 2, "Number of TCP ports to be used in ingress and egress rules")
	cmd.Flags().IntVar(&portRanges, "port-ranges", 2, "Number of TCP port ranges to be used in ingress and egress rules")
	cmd.Flags().IntVar(&remoteNamespaces, "remotes-namespaces", 2, "Number of remote namespaces to accept traffic from or send traffic to in ingress and egress rules")
	cmd.Flags().IntVar(&remotePods, "remotes-pods", 2, "Number of pods in remote namespaces to accept traffic from or send traffic to in ingress and egress rules")
	cmd.Flags().IntVar(&cidrs, "cidrs", 2, "Number of cidrs to accept traffic from or send traffic to in ingress and egress rules")
	cmd.Flags().BoolVar(&netpolLatency, "networkpolicy-latency", true, "Enable network policy latency measurement")
	cmd.Flags().IntVar(&convergenceTimeout, "convergence-timeout", 60, "Convergence timeout in seconds, provide integer value")
	cmd.Flags().IntVar(&convergencePeriod, "convergence-period", 180, "Convergence period in seconds, provide integer value")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"metrics-aggregated.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
