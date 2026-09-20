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
	"slices"
	"sync"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements/types"
	"github.com/kube-burner/kube-burner/v2/pkg/util/fileutils"
	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const (
	mnpLatencyMeasurementName          = "mnpLatencyMeasurement"
	mnpLatencyQuantilesMeasurementName = "mnpLatencyQuantilesMeasurement"
)

var (
	supportedMnpLatencyJobTypes = []config.JobType{config.CreationJob}
	mnpGVR                      = schema.GroupVersionResource{
		Group:    "k8s.cni.cncf.io",
		Version:  "v1beta1",
		Resource: "multi-networkpolicies",
	}
)

type mnpMetric struct {
	Timestamp      time.Time `json:"timestamp"`
	MetricName     string    `json:"metricName"`
	UUID           string    `json:"uuid"`
	JobName        string    `json:"jobName,omitempty"`
	Name           string    `json:"mnpName"`
	Namespace      string    `json:"namespace"`
	Metadata       any       `json:"metadata,omitempty"`
	CreatedLatency int       `json:"createdLatency"`
}

type mnpLatency struct {
	measurements.BaseMeasurement
	stopCh        chan struct{}
	dynamicClient dynamic.Interface
	startTime     time.Time
}

type mnpLatencyMeasurementFactory struct {
	measurements.BaseMeasurementFactory
}

func NewMnpLatencyMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]any, labelSelector string) (measurements.MeasurementFactory, error) {
	return mnpLatencyMeasurementFactory{
		measurements.NewBaseMeasurementFactory(configSpec, measurement, metadata, labelSelector),
	}, nil
}

func (mlmf mnpLatencyMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config, embedCfg *fileutils.EmbedConfiguration) measurements.Measurement {
	return &mnpLatency{
		BaseMeasurement: mlmf.NewBaseLatency(jobConfig, clientSet, restConfig, mnpLatencyMeasurementName, mnpLatencyQuantilesMeasurementName, embedCfg),
		dynamicClient:   dynamic.NewForConfigOrDie(restConfig),
	}
}

func (m *mnpLatency) handleAdd(obj any) {
	mnp := obj.(*unstructured.Unstructured)
	mnpName := mnp.GetName()
	mnpNamespace := mnp.GetNamespace()
	if mnpName == "" {
		log.Error("MultiNetworkPolicy object missing metadata.name, skipping")
		return
	}

	ts, found, _ := unstructured.NestedString(mnp.UnstructuredContent(), "metadata", "creationTimestamp")
	if !found || ts == "" {
		log.Errorf("MultiNetworkPolicy %s/%s missing creationTimestamp, skipping", mnpNamespace, mnpName)
		return
	}
	creationTime, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		log.Errorf("Error parsing MultiNetworkPolicy %s/%s creation timestamp: %v", mnpNamespace, mnpName, err)
		return
	}

	if creationTime.Before(m.startTime) {
		log.Debugf("Ignoring pre-existing MultiNetworkPolicy %s/%s (created before measurement start)", mnpNamespace, mnpName)
		return
	}

	latency := creationTime.Sub(m.startTime).Milliseconds()
	key := mnpNamespace + "/" + mnpName
	m.Metrics.LoadOrStore(key, mnpMetric{
		Name:           mnpName,
		Namespace:      mnpNamespace,
		Timestamp:      creationTime.UTC(),
		MetricName:     mnpLatencyMeasurementName,
		UUID:           m.Uuid,
		Metadata:       m.Metadata,
		JobName:        m.JobConfig.Name,
		CreatedLatency: int(latency),
	})
	log.Debugf("MultiNetworkPolicy %s/%s created, latency from job start: %dms", mnpNamespace, mnpName, latency)
}

func (m *mnpLatency) Start(measurementWg *sync.WaitGroup) error {
	defer measurementWg.Done()

	m.LatencyQuantiles, m.NormLatencies = nil, nil
	m.Metrics = sync.Map{}
	m.startTime = time.Now().UTC()

	if m.JobConfig.SkipIndexing {
		return nil
	}

	m.stopCh = make(chan struct{})
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(m.dynamicClient, 0, "", nil)
	mnpInformer := factory.ForResource(mnpGVR).Informer()
	mnpInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: m.handleAdd,
	})

	log.Infof("Starting MultiNetworkPolicy latency watcher for job %s", m.JobConfig.Name)
	factory.Start(m.stopCh)
	factory.WaitForCacheSync(m.stopCh)
	return nil
}

func (m *mnpLatency) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

func (m *mnpLatency) Stop() error {
	if m.JobConfig.SkipIndexing {
		return nil
	}
	close(m.stopCh)
	return m.StopMeasurement(m.normalizeMetrics, m.getLatency)
}

func (m *mnpLatency) normalizeMetrics() float64 {
	m.Metrics.Range(func(key, value any) bool {
		metric := value.(mnpMetric)
		m.NormLatencies = append(m.NormLatencies, metric)
		return true
	})
	return 0
}

func (m *mnpLatency) getLatency(normLatency any) map[string]float64 {
	metric := normLatency.(mnpMetric)
	return map[string]float64{
		"Created": float64(metric.CreatedLatency),
	}
}

func (m *mnpLatency) IsCompatible() bool {
	return slices.Contains(supportedMnpLatencyJobTypes, m.JobConfig.JobType)
}
