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
	cudnLatencyMeasurementName      = "cudnLatencyMeasurement"
	cudnLatencyQuantilesMeasurement = "cudnLatencyQuantilesMeasurement"
	cudnReadyConditionType          = "NetworkCreated"
)

var (
	supportedCudnLatencyJobTypes = []config.JobType{config.CreationJob}
	cudnGVRForLatency            = schema.GroupVersionResource{
		Group:    "k8s.ovn.org",
		Version:  "v1",
		Resource: "clusteruserdefinednetworks",
	}
)

type cudnMetric struct {
	Timestamp             time.Time `json:"timestamp"`
	MetricName            string    `json:"metricName"`
	UUID                  string    `json:"uuid"`
	JobName               string    `json:"jobName,omitempty"`
	Name                  string    `json:"cudnName"`
	Metadata              any       `json:"metadata,omitempty"`
	NetworkCreatedLatency int       `json:"networkAllocLatency"`
}

type cudnLatency struct {
	measurements.BaseMeasurement
	stopCh        chan struct{}
	dynamicClient dynamic.Interface
}

type cudnLatencyMeasurementFactory struct {
	measurements.BaseMeasurementFactory
}

func NewCudnLatencyMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]any, labelSelector string) (measurements.MeasurementFactory, error) {
	return cudnLatencyMeasurementFactory{
		measurements.NewBaseMeasurementFactory(configSpec, measurement, metadata, labelSelector),
	}, nil
}

func (clmf cudnLatencyMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config, embedCfg *fileutils.EmbedConfiguration) measurements.Measurement {
	return &cudnLatency{
		BaseMeasurement: clmf.NewBaseLatency(jobConfig, clientSet, restConfig, cudnLatencyMeasurementName, cudnLatencyQuantilesMeasurement, embedCfg),
		dynamicClient:   dynamic.NewForConfigOrDie(restConfig),
	}
}

func (c *cudnLatency) handleAdd(obj any) {
	cudn := obj.(*unstructured.Unstructured)
	cudnName, found, _ := unstructured.NestedString(cudn.UnstructuredContent(), "metadata", "name")
	if !found || cudnName == "" {
		log.Error("CUDN object missing metadata.name, skipping")
		return
	}
	ts, found, _ := unstructured.NestedString(cudn.UnstructuredContent(), "metadata", "creationTimestamp")
	if !found || ts == "" {
		log.Errorf("CUDN %s missing creationTimestamp, skipping", cudnName)
		return
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		log.Errorf("Error parsing CUDN %s creation timestamp: %v", cudnName, err)
		return
	}

	// Check if NetworkCreated is already True at creation time
	if transitionTime, ok := getNetworkAllocTransitionTime(cudn); ok {
		latency := transitionTime.Sub(t).Milliseconds()
		log.Debugf("CUDN %s already has NetworkCreated=True, latency: %dms", cudnName, latency)
		c.Metrics.LoadOrStore(cudnName, cudnMetric{
			Name:                  cudnName,
			Timestamp:             t.UTC(),
			MetricName:            cudnLatencyMeasurementName,
			UUID:                  c.Uuid,
			Metadata:              c.Metadata,
			JobName:               c.JobConfig.Name,
			NetworkCreatedLatency: int(latency),
		})
		return
	}

	// Store creation timestamp, latency will be computed on update
	c.Metrics.LoadOrStore(cudnName, cudnMetric{
		Name:                  cudnName,
		Timestamp:             t.UTC(),
		MetricName:            cudnLatencyMeasurementName,
		UUID:                  c.Uuid,
		Metadata:              c.Metadata,
		JobName:               c.JobConfig.Name,
		NetworkCreatedLatency: -1, // Not yet ready
	})
	log.Debugf("CUDN %s created at %v, waiting for NetworkCreated", cudnName, t.UTC())
}

func (c *cudnLatency) handleUpdate(oldObj, newObj any) {
	cudn := newObj.(*unstructured.Unstructured)
	cudnName, _, _ := unstructured.NestedString(cudn.UnstructuredContent(), "metadata", "name")

	transitionTime, succeeded := getNetworkAllocTransitionTime(cudn)
	if !succeeded {
		return
	}

	val, ok := c.Metrics.Load(cudnName)
	if !ok {
		return
	}
	m := val.(cudnMetric)
	if m.NetworkCreatedLatency >= 0 {
		return // Already recorded
	}

	latency := transitionTime.Sub(m.Timestamp).Milliseconds()
	m.NetworkCreatedLatency = int(latency)
	c.Metrics.Store(cudnName, m)
	log.Debugf("CUDN %s NetworkCreated after %dms", cudnName, latency)
}

// getNetworkAllocTransitionTime returns the lastTransitionTime of the
// NetworkCreated=True condition, and true if found.
func getNetworkAllocTransitionTime(cudn *unstructured.Unstructured) (time.Time, bool) {
	conditions, found, err := unstructured.NestedSlice(cudn.UnstructuredContent(), "status", "conditions")
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
		if condType == cudnReadyConditionType && condStatus == "True" {
			ltt, _, _ := unstructured.NestedString(condition, "lastTransitionTime")
			if t, err := time.Parse(time.RFC3339, ltt); err == nil {
				return t, true
			}
			// Fall back to current time if lastTransitionTime is missing
			log.Warnf("CUDN %s: NetworkCreated=True but missing lastTransitionTime, using current time", cudn.GetName())
			return time.Now().UTC(), true
		}
	}
	return time.Time{}, false
}

func (c *cudnLatency) Start(measurementWg *sync.WaitGroup) error {
	defer measurementWg.Done()

	c.LatencyQuantiles, c.NormLatencies = nil, nil
	c.Metrics = sync.Map{}

	if c.JobConfig.SkipIndexing {
		return nil
	}

	c.stopCh = make(chan struct{})
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(c.dynamicClient, 0, "", nil)
	cudnInformer := factory.ForResource(cudnGVRForLatency).Informer()
	cudnInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.handleAdd,
		UpdateFunc: c.handleUpdate,
	})

	log.Infof("Starting CUDN latency watcher for job %s", c.JobConfig.Name)
	factory.Start(c.stopCh)
	factory.WaitForCacheSync(c.stopCh)
	return nil
}

func (c *cudnLatency) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

func (c *cudnLatency) Stop() error {
	if c.JobConfig.SkipIndexing {
		return nil
	}
	close(c.stopCh)
	return c.StopMeasurement(c.normalizeMetrics, c.getLatency)
}

func (c *cudnLatency) normalizeMetrics() float64 {
	c.Metrics.Range(func(key, value any) bool {
		m := value.(cudnMetric)
		if m.NetworkCreatedLatency < 0 {
			log.Warnf("CUDN %s never reached NetworkCreated=True, excluding from latency metrics", m.Name)
			return true
		}
		c.NormLatencies = append(c.NormLatencies, m)
		return true
	})
	return 0
}

func (c *cudnLatency) getLatency(normLatency any) map[string]float64 {
	m := normLatency.(cudnMetric)
	return map[string]float64{
		"NetworkCreatedLatency": float64(m.NetworkCreatedLatency),
	}
}

func (c *cudnLatency) IsCompatible() bool {
	return slices.Contains(supportedCudnLatencyJobTypes, c.JobConfig.JobType)
}
