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
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const (
	ztwimSvidLatencyMeasurementName          = "ztwimSvidLatencyMeasurement"
	ztwimSvidLatencyQuantilesMeasurementName = "ztwimSvidLatencyQuantilesMeasurement"
)

var supportedZtwimSvidLatencyJobTypes = []config.JobType{config.CreationJob}

type ztwimSvidLatencyMetric struct {
	Timestamp    time.Time `json:"timestamp"`
	MetricName   string    `json:"metricName"`
	UUID         string    `json:"uuid"`
	JobName      string    `json:"jobName,omitempty"`
	PodName      string    `json:"podName"`
	Namespace    string    `json:"namespace"`
	SpiffeID     string    `json:"spiffeID,omitempty"`
	SvidLatency  int       `json:"svidLatencyMs"`
	Metadata     any       `json:"metadata,omitempty"`
}

type ztwimSvidLatency struct {
	measurements.BaseMeasurement
	stopCh      chan struct{}
	informerStop chan struct{}
	pollWg      sync.WaitGroup
	tracked     sync.Map
}

type ztwimSvidLatencyMeasurementFactory struct {
	measurements.BaseMeasurementFactory
}

func NewZtwimSvidLatencyMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]any, labelSelector string) (measurements.MeasurementFactory, error) {
	return ztwimSvidLatencyMeasurementFactory{
		measurements.NewBaseMeasurementFactory(configSpec, measurement, metadata, labelSelector),
	}, nil
}

func (f ztwimSvidLatencyMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config, embedCfg *fileutils.EmbedConfiguration) measurements.Measurement {
	return &ztwimSvidLatency{
		BaseMeasurement: f.NewBaseLatency(jobConfig, clientSet, restConfig, ztwimSvidLatencyMeasurementName, ztwimSvidLatencyQuantilesMeasurementName, embedCfg),
	}
}

func (z *ztwimSvidLatency) measurementConfig() (namespace, appContainer, appLabel string, maxWait, pollInterval time.Duration) {
	cfg := ZtwimSvidMeasurementConfig
	namespace = cfg.Namespace
	if namespace == "" {
		namespace = "ztwim-perf"
	}
	appContainer = cfg.AppContainer
	if appContainer == "" {
		appContainer = "app"
	}
	appLabel = cfg.AppLabel
	if appLabel == "" {
		appLabel = "ztwim-perf"
	}
	maxWait = cfg.SvidLatencyMaxWait
	if maxWait <= 0 {
		maxWait = 5 * time.Minute
	}
	pollInterval = cfg.SvidLatencyPollInterval
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	return namespace, appContainer, appLabel, maxWait, pollInterval
}

func (z *ztwimSvidLatency) Start(measurementWg *sync.WaitGroup) error {
	defer measurementWg.Done()
	z.LatencyQuantiles, z.NormLatencies = nil, nil
	z.Metrics = sync.Map{}
	z.tracked = sync.Map{}
	z.stopCh = make(chan struct{})
	z.informerStop = make(chan struct{})

	if z.JobConfig.SkipIndexing {
		return nil
	}

	namespace, _, appLabel, _, _ := z.measurementConfig()
	labelSel := ztwimPodLabelSelector(appLabel, z.LabelSelector)

	tweak := func(lo *metav1.ListOptions) {
		lo.LabelSelector = labelSel
	}
	factory := informers.NewSharedInformerFactoryWithOptions(z.ClientSet, 0, informers.WithNamespace(namespace), informers.WithTweakListOptions(tweak))
	informer := factory.Core().V1().Pods().Informer()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return
			}
			z.startPollingPod(pod)
		},
	})

	log.Infof("Starting SVID latency watcher for job %s in namespace %s", z.JobConfig.Name, namespace)
	factory.Start(z.informerStop)
	factory.WaitForCacheSync(z.informerStop)
	return nil
}

func (z *ztwimSvidLatency) startPollingPod(pod *corev1.Pod) {
	if _, loaded := z.tracked.LoadOrStore(pod.UID, true); loaded {
		return
	}
	z.pollWg.Add(1)
	go z.pollPodUntilSVID(pod)
}

func (z *ztwimSvidLatency) pollPodUntilSVID(pod *corev1.Pod) {
	defer z.pollWg.Done()

	namespace, appContainer, _, maxWait, pollInterval := z.measurementConfig()
	created := pod.CreationTimestamp.Time
	deadline := created.Add(maxWait)

	for {
		select {
		case <-z.stopCh:
			return
		default:
		}
		if time.Now().After(deadline) {
			log.Warnf("pod %s: timed out waiting for svid.pem after %v", pod.Name, maxWait)
			z.Metrics.Store(string(pod.UID), ztwimSvidLatencyMetric{
				Timestamp:   time.Now().UTC(),
				MetricName:  ztwimSvidLatencyMeasurementName,
				UUID:        z.Uuid,
				Metadata:    z.Metadata,
				JobName:     z.JobConfig.Name,
				PodName:     pod.Name,
				Namespace:   pod.Namespace,
				SvidLatency: -1,
			})
			return
		}

		latest, err := z.ClientSet.CoreV1().Pods(namespace).Get(context.TODO(), pod.Name, metav1.GetOptions{})
		if err != nil {
			log.Debugf("pod %s: get status: %v", pod.Name, err)
			time.Sleep(pollInterval)
			continue
		}
		if latest.Status.Phase == corev1.PodRunning {
			ok, spiffeID, err := svidPresentInPod(z.ClientSet, z.RestConfig, namespace, pod.Name, appContainer)
			if ok {
				latencyMs := int(time.Since(created).Milliseconds())
				log.Debugf("pod %s: svid.pem present after %dms", pod.Name, latencyMs)
				z.Metrics.Store(string(pod.UID), ztwimSvidLatencyMetric{
					Timestamp:   time.Now().UTC(),
					MetricName:  ztwimSvidLatencyMeasurementName,
					UUID:        z.Uuid,
					Metadata:    z.Metadata,
					JobName:     z.JobConfig.Name,
					PodName:     pod.Name,
					Namespace:   pod.Namespace,
					SpiffeID:    spiffeID,
					SvidLatency: latencyMs,
				})
				return
			}
			if err != nil {
				log.Debugf("pod %s: svid not ready yet: %v", pod.Name, err)
			}
		}

		time.Sleep(pollInterval)
	}
}

func (z *ztwimSvidLatency) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

func (z *ztwimSvidLatency) Stop() error {
	if z.JobConfig.SkipIndexing {
		return nil
	}
	namespace, _, appLabel, maxWait, pollInterval := z.measurementConfig()
	labelSel := ztwimPodLabelSelector(appLabel, z.LabelSelector)
	pods, err := z.ClientSet.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSel,
	})
	if err != nil {
		return fmt.Errorf("listing pods for SVID latency: %w", err)
	}
	{
		for i := range pods.Items {
			z.startPollingPod(&pods.Items[i])
		}
		deadline := time.Now().Add(maxWait)
		for time.Now().Before(deadline) {
			done := 0
			z.Metrics.Range(func(_, value any) bool {
				m := value.(ztwimSvidLatencyMetric)
				if m.SvidLatency >= 0 {
					done++
				}
				return true
			})
			if len(pods.Items) > 0 && done >= len(pods.Items) {
				break
			}
			time.Sleep(pollInterval)
		}
	}
	if z.informerStop != nil {
		close(z.informerStop)
	}
	if z.stopCh != nil {
		close(z.stopCh)
	}
	z.pollWg.Wait()

	var failures int
	z.Metrics.Range(func(_, value any) bool {
		m := value.(ztwimSvidLatencyMetric)
		if m.SvidLatency < 0 {
			failures++
			return true
		}
		z.NormLatencies = append(z.NormLatencies, m)
		return true
	})

	tracked := 0
	z.Metrics.Range(func(_, _ any) bool {
		tracked++
		return true
	})
	expectedPods := len(pods.Items)
	if expectedPods > tracked {
		failures += expectedPods - tracked
	}
	errorRate := 0.0
	if expectedPods > 0 {
		errorRate = float64(failures) / float64(expectedPods) * 100
	}
	log.Infof("%s: SVID latency collected for %d pods (%d failures)", z.JobConfig.Name, len(z.NormLatencies), failures)
	stopErr := z.StopMeasurement(func() float64 { return errorRate }, z.getLatency)
	if failures > 0 {
		return fmt.Errorf("%s: %d SVID latency failures (expected %d pods)", z.JobConfig.Name, failures, expectedPods)
	}
	return stopErr
}

func (z *ztwimSvidLatency) getLatency(normLatency any) map[string]float64 {
	m := normLatency.(ztwimSvidLatencyMetric)
	return map[string]float64{
		"SvidLatency": float64(m.SvidLatency),
	}
}

func (z *ztwimSvidLatency) IsCompatible() bool {
	return slices.Contains(supportedZtwimSvidLatencyJobTypes, z.JobConfig.JobType)
}
