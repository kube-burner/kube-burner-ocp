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
	"context"
	"crypto/x509"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements/types"
	"github.com/kube-burner/kube-burner/v2/pkg/util/fileutils"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	ztwimSvidVerificationMeasurementName          = "ztwimSvidVerificationMeasurement"
	ztwimSvidVerificationQuantilesMeasurementName = "ztwimSvidVerificationQuantilesMeasurement"
)

var supportedZtwimSvidVerificationJobTypes = []config.JobType{config.CreationJob}

type ztwimSvidPodCert struct {
	cert     *x509.Certificate
	spiffeID string
}

type ztwimSvidVerificationMetric struct {
	Timestamp         time.Time `json:"timestamp"`
	MetricName        string    `json:"metricName"`
	UUID              string    `json:"uuid"`
	JobName           string    `json:"jobName,omitempty"`
	PodName           string    `json:"podName"`
	SpiffeID          string    `json:"spiffeID"`
	SvidAgeMs         int       `json:"svidAgeMs"`
	RotationObserved  int       `json:"rotationObserved"`
	DistinctSpiffeIDs int       `json:"distinctSpiffeIDs,omitempty"`
	Metadata          any       `json:"metadata,omitempty"`
}

type ztwimSvidVerification struct {
	measurements.BaseMeasurement
}

type ztwimSvidVerificationMeasurementFactory struct {
	measurements.BaseMeasurementFactory
}

func NewZtwimSvidVerificationMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]any, labelSelector string) (measurements.MeasurementFactory, error) {
	return ztwimSvidVerificationMeasurementFactory{
		measurements.NewBaseMeasurementFactory(configSpec, measurement, metadata, labelSelector),
	}, nil
}

func (f ztwimSvidVerificationMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config, embedCfg *fileutils.EmbedConfiguration) measurements.Measurement {
	return &ztwimSvidVerification{
		BaseMeasurement: f.NewBaseLatency(jobConfig, clientSet, restConfig, ztwimSvidVerificationMeasurementName, ztwimSvidVerificationQuantilesMeasurementName, embedCfg),
	}
}

func (z *ztwimSvidVerification) Start(measurementWg *sync.WaitGroup) error {
	defer measurementWg.Done()
	z.LatencyQuantiles, z.NormLatencies = nil, nil
	z.Metrics = sync.Map{}
	return nil
}

func (z *ztwimSvidVerification) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

func (z *ztwimSvidVerification) Stop() error {
	if z.JobConfig.SkipIndexing {
		return nil
	}

	var namespace, appContainer, appLabel string
	ztwimSvidWorkloadTargets(&namespace, &appContainer, &appLabel)
	maxWait, pollInterval := ztwimSvidPollSettings(0, 0)
	podLabelSelector := ztwimPodLabelSelector(appLabel, z.LabelSelector)

	verified, err := z.waitForAttestationSVIDs(namespace, appContainer, podLabelSelector, maxWait, pollInterval)
	if err != nil {
		return err
	}

	pods, err := z.listAttestationPods(namespace, podLabelSelector)
	if err != nil {
		return err
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("no attestation pods found in %s", namespace)
	}
	if len(verified) < len(pods.Items) {
		log.Warnf("%s: only %d/%d pods have SVID after %v; continuing verification", z.JobConfig.Name, len(verified), len(pods.Items), maxWait)
	}

	cfg := ZtwimSvidMeasurementConfig
	rotationMaxAge := cfg.SVIDTTL + 30*time.Second
	if cfg.RotationSoak == 0 {
		rotationMaxAge = 0
	}

	failures := z.recordVerificationMetrics(pods.Items, verified, namespace, appContainer, rotationMaxAge, cfg.RotationSoak > 0)

	errorRate := 0.0
	if len(pods.Items) > 0 {
		errorRate = float64(failures) / float64(len(pods.Items)) * 100
	}
	stopErr := z.StopMeasurement(func() float64 { return errorRate }, z.getLatency)
	if failures > 0 {
		return fmt.Errorf("%s: %d SVID verification failures", z.JobConfig.Name, failures)
	}
	return stopErr
}

func (z *ztwimSvidVerification) listAttestationPods(namespace, podLabelSelector string) (*corev1.PodList, error) {
	pods, err := z.ClientSet.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: podLabelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("listing attestation pods: %w", err)
	}
	return pods, nil
}

func (z *ztwimSvidVerification) waitForAttestationSVIDs(namespace, appContainer, podLabelSelector string, maxWait, pollInterval time.Duration) (map[string]ztwimSvidPodCert, error) {
	deadline := time.Now().Add(maxWait)
	verified := map[string]ztwimSvidPodCert{}

	for time.Now().Before(deadline) {
		pods, err := z.listAttestationPods(namespace, podLabelSelector)
		if err != nil {
			return nil, err
		}
		if len(pods.Items) == 0 {
			time.Sleep(pollInterval)
			continue
		}

		podSetComplete := true
		for _, pod := range pods.Items {
			if _, done := verified[pod.Name]; done {
				continue
			}
			if pod.Status.Phase != corev1.PodRunning {
				podSetComplete = false
				continue
			}
			pemBytes, err := readFileFromPod(z.ClientSet, z.RestConfig, namespace, pod.Name, appContainer, "/certs/svid.pem")
			if err != nil {
				log.Debugf("pod %s: svid not ready yet: %v", pod.Name, err)
				podSetComplete = false
				continue
			}
			cert, spiffeID, err := parseSVIDCertificate(pemBytes)
			if err != nil {
				log.Debugf("pod %s: svid not valid yet: %v", pod.Name, err)
				podSetComplete = false
				continue
			}
			verified[pod.Name] = ztwimSvidPodCert{cert: cert, spiffeID: spiffeID}
		}
		if podSetComplete && len(verified) == len(pods.Items) {
			break
		}
		time.Sleep(pollInterval)
	}
	return verified, nil
}

func (z *ztwimSvidVerification) readPodSVID(namespace, appContainer, podName string) (*x509.Certificate, string, error) {
	pemBytes, err := readFileFromPod(z.ClientSet, z.RestConfig, namespace, podName, appContainer, "/certs/svid.pem")
	if err != nil {
		return nil, "", err
	}
	return parseSVIDCertificate(pemBytes)
}

func (z *ztwimSvidVerification) recordVerificationMetrics(pods []corev1.Pod, verified map[string]ztwimSvidPodCert, namespace, appContainer string, rotationMaxAge time.Duration, rotationSoakEnabled bool) int {
	spiffeIDs := map[string]struct{}{}
	var failures int

	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodRunning {
			log.Warnf("pod %s phase %s, skipping SVID verification", pod.Name, pod.Status.Phase)
			failures++
			continue
		}

		var cert *x509.Certificate
		var spiffeID string
		var err error
		if rotationSoakEnabled {
			cert, spiffeID, err = z.readPodSVID(namespace, appContainer, pod.Name)
			if err != nil {
				log.Warnf("pod %s: %v", pod.Name, err)
				failures++
				continue
			}
		} else {
			v, ok := verified[pod.Name]
			if !ok {
				log.Warnf("pod %s: no SVID after wait", pod.Name)
				failures++
				continue
			}
			cert, spiffeID = v.cert, v.spiffeID
		}
		spiffeIDs[spiffeID] = struct{}{}

		age := time.Since(cert.NotBefore)
		rotationObserved := 0
		if rotationMaxAge > 0 {
			if age <= rotationMaxAge {
				rotationObserved = 1
			} else {
				log.Warnf("pod %s: SVID age %v exceeds rotation window %v (TTL %v); rotation may not have occurred during soak", pod.Name, age.Round(time.Second), rotationMaxAge, ZtwimSvidMeasurementConfig.SVIDTTL)
				failures++
			}
		}

		z.NormLatencies = append(z.NormLatencies, ztwimSvidVerificationMetric{
			Timestamp:        time.Now().UTC(),
			MetricName:       ztwimSvidVerificationMeasurementName,
			UUID:             z.Uuid,
			Metadata:         z.Metadata,
			JobName:          z.JobConfig.Name,
			PodName:          pod.Name,
			SpiffeID:         spiffeID,
			SvidAgeMs:        int(age.Milliseconds()),
			RotationObserved: rotationObserved,
		})
	}

	distinct := len(spiffeIDs)
	log.Infof("%s: verified %d pods, %d distinct SPIFFE IDs, %d failures", z.JobConfig.Name, len(z.NormLatencies), distinct, failures)
	if len(z.NormLatencies) > 0 && distinct != len(z.NormLatencies) {
		log.Warnf("expected %d distinct SPIFFE IDs, got %d", len(z.NormLatencies), distinct)
		failures++
	}
	for i := range z.NormLatencies {
		m := z.NormLatencies[i].(ztwimSvidVerificationMetric)
		m.DistinctSpiffeIDs = distinct
		z.NormLatencies[i] = m
	}
	return failures
}

func (z *ztwimSvidVerification) getLatency(normLatency any) map[string]float64 {
	m := normLatency.(ztwimSvidVerificationMetric)
	return map[string]float64{
		"SvidAge":          float64(m.SvidAgeMs),
		"RotationObserved": float64(m.RotationObserved),
	}
}

func (z *ztwimSvidVerification) IsCompatible() bool {
	return slices.Contains(supportedZtwimSvidVerificationJobTypes, z.JobConfig.JobType)
}
