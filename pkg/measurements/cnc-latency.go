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
	cncLatencyMeasurementName      = "cncLatencyMeasurement"
	cncLatencyQuantilesMeasurement = "cncLatencyQuantilesMeasurement"
	cncAcceptedConditionType       = "Accepted"
)

var (
	supportedCncLatencyJobTypes = []config.JobType{config.CreationJob}
	cncGVR                      = schema.GroupVersionResource{
		Group:    "k8s.ovn.org",
		Version:  "v1",
		Resource: "clusternetworkconnects",
	}
)

type cncMetric struct {
	Timestamp       time.Time `json:"timestamp"`
	MetricName      string    `json:"metricName"`
	UUID            string    `json:"uuid"`
	JobName         string    `json:"jobName,omitempty"`
	Name            string    `json:"cncName"`
	Metadata        any       `json:"metadata,omitempty"`
	AcceptedLatency int       `json:"acceptedLatency"`
}

type cncLatency struct {
	measurements.BaseMeasurement
	stopCh        chan struct{}
	dynamicClient dynamic.Interface
}

type cncLatencyMeasurementFactory struct {
	measurements.BaseMeasurementFactory
}

func NewCncLatencyMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]any, labelSelector string) (measurements.MeasurementFactory, error) {
	return cncLatencyMeasurementFactory{
		measurements.NewBaseMeasurementFactory(configSpec, measurement, metadata, labelSelector),
	}, nil
}

func (f cncLatencyMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config, embedCfg *fileutils.EmbedConfiguration) measurements.Measurement {
	return &cncLatency{
		BaseMeasurement: f.NewBaseLatency(jobConfig, clientSet, restConfig, cncLatencyMeasurementName, cncLatencyQuantilesMeasurement, embedCfg),
		dynamicClient:   dynamic.NewForConfigOrDie(restConfig),
	}
}

func (c *cncLatency) handleAdd(obj any) {
	cnc := obj.(*unstructured.Unstructured)
	cncName, found, _ := unstructured.NestedString(cnc.UnstructuredContent(), "metadata", "name")
	if !found || cncName == "" {
		log.Error("CNC object missing metadata.name, skipping")
		return
	}
	ts, found, _ := unstructured.NestedString(cnc.UnstructuredContent(), "metadata", "creationTimestamp")
	if !found || ts == "" {
		log.Errorf("CNC %s missing creationTimestamp, skipping", cncName)
		return
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		log.Errorf("Error parsing CNC %s creation timestamp: %v", cncName, err)
		return
	}

	if transitionTime, ok := getCncAcceptedTransitionTime(cnc); ok {
		latency := transitionTime.Sub(t).Milliseconds()
		log.Debugf("CNC %s already has Accepted=True, latency: %dms", cncName, latency)
		c.Metrics.LoadOrStore(cncName, cncMetric{
			Name:            cncName,
			Timestamp:       t.UTC(),
			MetricName:      cncLatencyMeasurementName,
			UUID:            c.Uuid,
			Metadata:        c.Metadata,
			JobName:         c.JobConfig.Name,
			AcceptedLatency: int(latency),
		})
		return
	}

	c.Metrics.LoadOrStore(cncName, cncMetric{
		Name:            cncName,
		Timestamp:       t.UTC(),
		MetricName:      cncLatencyMeasurementName,
		UUID:            c.Uuid,
		Metadata:        c.Metadata,
		JobName:         c.JobConfig.Name,
		AcceptedLatency: -1,
	})
	log.Debugf("CNC %s created at %v, waiting for Accepted", cncName, t.UTC())
}

func (c *cncLatency) handleUpdate(oldObj, newObj any) {
	cnc := newObj.(*unstructured.Unstructured)
	cncName, _, _ := unstructured.NestedString(cnc.UnstructuredContent(), "metadata", "name")

	transitionTime, succeeded := getCncAcceptedTransitionTime(cnc)
	if !succeeded {
		return
	}

	val, ok := c.Metrics.Load(cncName)
	if !ok {
		return
	}
	m := val.(cncMetric)
	if m.AcceptedLatency >= 0 {
		return
	}

	latency := transitionTime.Sub(m.Timestamp).Milliseconds()
	m.AcceptedLatency = int(latency)
	c.Metrics.Store(cncName, m)
	log.Debugf("CNC %s Accepted after %dms", cncName, latency)
}

func getCncAcceptedTransitionTime(cnc *unstructured.Unstructured) (time.Time, bool) {
	conditions, found, err := unstructured.NestedSlice(cnc.UnstructuredContent(), "status", "conditions")
	if err != nil || !found {
		return time.Time{}, false
	}
	for _, c := range conditions {
		condition, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		condType, _, _ := unstructured.NestedString(condition, "type")
		condStatus, _, _ := unstructured.NestedString(condition, "status")
		if condType == cncAcceptedConditionType && condStatus == "True" {
			ltt, _, _ := unstructured.NestedString(condition, "lastTransitionTime")
			if t, err := time.Parse(time.RFC3339, ltt); err == nil {
				return t, true
			}
			log.Warnf("CNC %s: Accepted=True but missing lastTransitionTime, using current time", cnc.GetName())
			return time.Now().UTC(), true
		}
	}
	return time.Time{}, false
}

func (c *cncLatency) Start(measurementWg *sync.WaitGroup) error {
	defer measurementWg.Done()

	c.LatencyQuantiles, c.NormLatencies = nil, nil
	c.Metrics = sync.Map{}

	if c.JobConfig.SkipIndexing {
		return nil
	}

	c.stopCh = make(chan struct{})
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(c.dynamicClient, 0, "", nil)
	cncInformer := factory.ForResource(cncGVR).Informer()
	cncInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.handleAdd,
		UpdateFunc: c.handleUpdate,
	})

	log.Infof("Starting CNC latency watcher for job %s", c.JobConfig.Name)
	factory.Start(c.stopCh)
	factory.WaitForCacheSync(c.stopCh)
	return nil
}

func (c *cncLatency) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

func (c *cncLatency) Stop() error {
	if c.JobConfig.SkipIndexing {
		return nil
	}
	close(c.stopCh)
	return c.StopMeasurement(c.normalizeMetrics, c.getLatency)
}

func (c *cncLatency) normalizeMetrics() float64 {
	c.Metrics.Range(func(key, value any) bool {
		m := value.(cncMetric)
		if m.AcceptedLatency < 0 {
			log.Warnf("CNC %s never reached Accepted=True, excluding from latency metrics", m.Name)
			return true
		}
		c.NormLatencies = append(c.NormLatencies, m)
		return true
	})
	return 0
}

func (c *cncLatency) getLatency(normLatency any) map[string]float64 {
	m := normLatency.(cncMetric)
	return map[string]float64{
		"AcceptedLatency": float64(m.AcceptedLatency),
	}
}

func (c *cncLatency) IsCompatible() bool {
	return slices.Contains(supportedCncLatencyJobTypes, c.JobConfig.JobType)
}
