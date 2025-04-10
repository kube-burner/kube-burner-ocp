// Copyright 2020 The Kube-burner Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package ocp

import (
	"sync"
	"time"

	"github.com/cloud-bulldozer/go-commons/v2/indexers"
	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/measurements"
	"github.com/kube-burner/kube-burner/pkg/measurements/metrics"
	"github.com/kube-burner/kube-burner/pkg/measurements/types"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	exampleLatencyMeasurement          = "exampleLatencyMeasurement"
	exampleLatencyQuantilesMeasurement = "exampleLatencyQuantilesMeasurement"
)

type exampleMetric struct {
	Timestamp    time.Time   `json:"timestamp"`
	MetricName   string      `json:"metricName"`
	UUID         string      `json:"uuid"`
	JobName      string      `json:"jobName,omitempty"`
	JobIteration int         `json:"jobIteration"`
	Replica      int         `json:"replica"`
	Namespace    string      `json:"namespace"`
	Name         string      `json:"exampleName"`
	Metadata     interface{} `json:"metadata,omitempty"`
}

type exampleLatency struct {
	measurements.BaseLatencyMeasurement

	metrics          sync.Map
	latencyQuantiles []interface{}
	normLatencies    []interface{}
}

type exampleLatencyMeasurementFactory struct {
	measurements.BaseLatencyMeasurementFactory
}

func NewExampleLatencyMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]interface{}) (measurements.MeasurementFactory, error) {
	return exampleLatencyMeasurementFactory{
		measurements.NewBaseLatencyMeasurementFactory(configSpec, measurement, metadata),
	}, nil
}

func (plmf exampleLatencyMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config) measurements.Measurement {
	return &exampleLatency{
		BaseLatencyMeasurement: plmf.NewBaseLatency(jobConfig, clientSet, restConfig),
	}
}

// start exampleLatency measurement
func (p *exampleLatency) Start(measurementWg *sync.WaitGroup) error {
	// Reset latency slices, required in multi-job benchmarks
	p.latencyQuantiles, p.normLatencies = nil, nil
	defer measurementWg.Done()
	p.metrics = sync.Map{}
	log.Infof("Creating Example latency watcher for %s", p.JobConfig.Name)
	p.metrics.LoadOrStore("example-1", exampleMetric{
		Timestamp:    time.Now().UTC(),
		Namespace:    "example-1",
		Name:         "example-1",
		MetricName:   exampleLatencyMeasurement,
		UUID:         "example-1",
		JobName:      p.JobConfig.Name,
		Metadata:     p.Metadata,
		JobIteration: 0,
		Replica:      1,
	})
	return nil
}

// collects example measurements triggered in the past
func (p *exampleLatency) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

// Stop stops exampleLatency measurement
func (p *exampleLatency) Stop() error {
	var err error
	p.normalizeMetrics()
	for _, q := range p.latencyQuantiles {
		pq := q.(metrics.LatencyQuantiles)
		log.Infof("%s: %v 99th: %v max: %v avg: %v", p.JobConfig.Name, pq.QuantileName, pq.P99, pq.Max, pq.Avg)
	}
	return err
}

// index sends metrics to the configured indexer
func (p *exampleLatency) Index(jobName string, indexerList map[string]indexers.Indexer) {
	metricMap := map[string][]interface{}{
		exampleLatencyMeasurement:          p.normLatencies,
		exampleLatencyQuantilesMeasurement: p.latencyQuantiles,
	}
	measurements.IndexLatencyMeasurement(p.Config, jobName, metricMap, indexerList)
}

func (p *exampleLatency) GetMetrics() *sync.Map {
	return &p.metrics
}

func (p *exampleLatency) normalizeMetrics() float64 {
	p.metrics.Range(func(key, value interface{}) bool {
		m := value.(exampleMetric)
		p.normLatencies = append(p.normLatencies, m)
		return true
	})
	return 0.0
}
