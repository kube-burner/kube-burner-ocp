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
	ztwimSvidVerificationMeasurementName        = "ztwimSvidVerificationMeasurement"
	ztwimSvidVerificationQuantilesMeasurementName = "ztwimSvidVerificationQuantilesMeasurement"
)

var supportedZtwimSvidVerificationJobTypes = []config.JobType{config.CreationJob}

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
	cfg := ZtwimSvidMeasurementConfig
	if cfg.Namespace == "" {
		cfg.Namespace = "ztwim-perf"
	}
	if cfg.AppContainer == "" {
		cfg.AppContainer = "app"
	}
	if cfg.AppLabel == "" {
		cfg.AppLabel = "ztwim-perf"
	}

	maxWait := cfg.SvidLatencyMaxWait
	if maxWait <= 0 {
		maxWait = 5 * time.Minute
	}
	pollInterval := cfg.SvidLatencyPollInterval
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}

	deadline := time.Now().Add(maxWait)
	verified := map[string]struct {
		cert     *x509.Certificate
		spiffeID string
	}{}

	podLabelSelector := ztwimPodLabelSelector(cfg.AppLabel, z.LabelSelector)

	for time.Now().Before(deadline) {
		pods, err := z.ClientSet.CoreV1().Pods(cfg.Namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: podLabelSelector,
		})
		if err != nil {
			return fmt.Errorf("listing attestation pods: %w", err)
		}
		if len(pods.Items) == 0 {
			time.Sleep(pollInterval)
			continue
		}

		allReady := true
		for _, pod := range pods.Items {
			if _, done := verified[pod.Name]; done {
				continue
			}
			if pod.Status.Phase != corev1.PodRunning {
				allReady = false
				continue
			}
			pemBytes, err := readFileFromPod(z.ClientSet, z.RestConfig, cfg.Namespace, pod.Name, cfg.AppContainer, "/certs/svid.pem")
			if err != nil {
				log.Debugf("pod %s: svid not ready yet: %v", pod.Name, err)
				allReady = false
				continue
			}
			cert, spiffeID, err := parseSVIDCertificate(pemBytes)
			if err != nil {
				log.Debugf("pod %s: svid not valid yet: %v", pod.Name, err)
				allReady = false
				continue
			}
			verified[pod.Name] = struct {
				cert     *x509.Certificate
				spiffeID string
			}{cert: cert, spiffeID: spiffeID}
		}
		if allReady && len(verified) == len(pods.Items) {
			break
		}
		time.Sleep(pollInterval)
	}

	pods, err := z.ClientSet.CoreV1().Pods(cfg.Namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: podLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("listing attestation pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("no attestation pods found in %s", cfg.Namespace)
	}
	if len(verified) < len(pods.Items) {
		log.Warnf("%s: only %d/%d pods have SVID after %v; continuing verification", z.JobConfig.Name, len(verified), len(pods.Items), maxWait)
	}

	rotationMaxAge := cfg.SVIDTTL + 30*time.Second
	if cfg.RotationSoak == 0 {
		rotationMaxAge = 0
	}

	spiffeIDs := map[string]struct{}{}
	var failures int

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			log.Warnf("pod %s phase %s, skipping SVID verification", pod.Name, pod.Status.Phase)
			failures++
			continue
		}
		var cert *x509.Certificate
		var spiffeID string
		if cfg.RotationSoak > 0 {
			pemBytes, err := readFileFromPod(z.ClientSet, z.RestConfig, cfg.Namespace, pod.Name, cfg.AppContainer, "/certs/svid.pem")
			if err != nil {
				log.Warnf("pod %s: %v", pod.Name, err)
				failures++
				continue
			}
			cert, spiffeID, err = parseSVIDCertificate(pemBytes)
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
				log.Warnf("pod %s: SVID age %v exceeds rotation window %v (TTL %v); rotation may not have occurred during soak", pod.Name, age.Round(time.Second), rotationMaxAge, cfg.SVIDTTL)
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
