//go:build linux
// +build linux

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
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
)

func readFileFromPod(clientSet kubernetes.Interface, restConfig *rest.Config, namespace, podName, container, path string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	req := clientSet.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec")
	req.VersionedParams(&corev1.PodExecOptions{
		Container: container,
		Command:   []string{"cat", path},
		Stdout:    true,
		Stderr:    true,
	}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(restConfig, "POST", req.URL())
	if err != nil {
		return nil, err
	}
	if err := exec.StreamWithContext(context.TODO(), remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}); err != nil {
		return nil, fmt.Errorf("exec cat %s: %w (stderr: %s)", path, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func parseSVIDCertificate(pemBytes []byte) (*x509.Certificate, string, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, "", fmt.Errorf("failed to decode PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, "", err
	}
	for _, u := range cert.URIs {
		if u != nil && u.Scheme == "spiffe" {
			return cert, u.String(), nil
		}
	}
	if len(cert.URIs) > 0 && cert.URIs[0] != nil {
		return cert, cert.URIs[0].String(), nil
	}
	return cert, "", fmt.Errorf("no SPIFFE URI in certificate")
}

func ztwimPodLabelSelector(appLabel, kubeBurnerRunSelector string) string {
	labelSel := "app=" + appLabel
	if kubeBurnerRunSelector != "" {
		labelSel = labelSel + "," + kubeBurnerRunSelector
	}
	return labelSel
}

func svidPresentInPod(clientSet kubernetes.Interface, restConfig *rest.Config, namespace, podName, container string) (bool, string, error) {
	pemBytes, err := readFileFromPod(clientSet, restConfig, namespace, podName, container, "/certs/svid.pem")
	if err != nil {
		return false, "", err
	}
	_, spiffeID, err := parseSVIDCertificate(pemBytes)
	if err != nil {
		return false, "", err
	}
	return true, spiffeID, nil
}
