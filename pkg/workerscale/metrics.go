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

package workerscale

import (
	"strings"
	"sync"
	"time"

	"github.com/cloud-bulldozer/go-commons/indexers"
	"github.com/kube-burner/kube-burner/pkg/config"
	"github.com/kube-burner/kube-burner/pkg/measurements"
	mmetrics "github.com/kube-burner/kube-burner/pkg/measurements/metrics"
	mtypes "github.com/kube-burner/kube-burner/pkg/measurements/types"
	log "github.com/sirupsen/logrus"
)

// setupMetrics sets up the measurment factory for us
func setupMetrics(uuid string, metadata map[string]interface{}, kubeClientProvider *config.KubeClientProvider) {
	configSpec := config.Spec{
		GlobalConfig: config.GlobalConfig{
			UUID: uuid,
			Measurements: []mtypes.Measurement{
				{
					Name: measurementName,
				},
			},
		},
	}
	measurements.NewMeasurementFactory(configSpec, metadata)
	measurements.SetJobConfig(
		&config.Job{
			Name: JobName,
		},
		kubeClientProvider,
	)
}

// finalizeMetrics performs and indexes required metrics
func finalizeMetrics(machineSetsToEdit *sync.Map, scaledMachineDetails map[string]MachineInfo, indexerValue indexers.Indexer, amiID string, scaleEventEpoch int64) {
	nodeMetrics := measurements.GetMetrics()
	normLatencies, latencyQuantiles := calculateMetrics(machineSetsToEdit, scaledMachineDetails, nodeMetrics[0], amiID, scaleEventEpoch)
	for _, q := range latencyQuantiles {
		nq := q.(mmetrics.LatencyQuantiles)
		log.Infof("%s: %s 50th: %v 99th: %v max: %v avg: %v", JobName, nq.QuantileName, nq.P50, nq.P99, nq.Max, nq.Avg)
	}
	metricMap := map[string][]interface{}{
		nodeReadyLatencyMeasurement:          normLatencies,
		nodeReadyLatencyQuantilesMeasurement: latencyQuantiles,
	}
	measurements.IndexLatencyMeasurement(mtypes.Measurement{Name: measurementName}, JobName, metricMap, map[string]indexers.Indexer{
		"": indexerValue,
	})
}

// calculateMetrics calculates the metrics for node bootup times
func calculateMetrics(machineSetsToEdit *sync.Map, scaledMachineDetails map[string]MachineInfo, nodeMetrics *sync.Map, amiID string, scaleEventEpoch int64) ([]interface{}, []interface{}) {
	var scaleEventTimestamp time.Time
	var uuid, machineSetName string
	var normLatencies, latencyQuantiles []interface{}
	for machine, info := range scaledMachineDetails {
		lastHypenIndex := strings.LastIndex(machine, "-")
		if lastHypenIndex != (-1) {
			machineSetName = machine[:lastHypenIndex]
		}
		if _, exists := nodeMetrics.Load(info.nodeUID); !exists {
			continue
		}
		if scaleEventEpoch == 0 {
			msValue, _ := machineSetsToEdit.Load(machineSetName)
			msInfo := msValue.(MachineSetInfo)
			scaleEventTimestamp = msInfo.lastUpdatedTime
		} else {
			scaleEventTimestamp = time.Unix(scaleEventEpoch, 0).UTC()
		}
		machineCreationTimeStamp := info.creationTimestamp
		machineReadyTimeStamp := info.readyTimestamp
		nmValue, _ := nodeMetrics.Load(info.nodeUID)
		nodeMetricValue := nmValue.(measurements.NodeMetric)
		uuid = nodeMetricValue.UUID
		normLatencies = append(normLatencies, NodeReadyMetric{
			ScaleEventTimestamp:      scaleEventTimestamp,
			MachineCreationTimestamp: machineCreationTimeStamp,
			MachineCreationLatency:   int(machineCreationTimeStamp.Sub(scaleEventTimestamp).Milliseconds()),
			MachineReadyTimestamp:    machineReadyTimeStamp,
			MachineReadyLatency:      int(machineReadyTimeStamp.Sub(scaleEventTimestamp).Milliseconds()),
			NodeCreationTimestamp:    nodeMetricValue.Timestamp,
			NodeCreationLatency:      int(nodeMetricValue.Timestamp.Sub(scaleEventTimestamp).Milliseconds()),
			NodeReadyTimestamp:       nodeMetricValue.NodeReady,
			NodeReadyLatency:         int(nodeMetricValue.NodeReady.Sub(scaleEventTimestamp).Milliseconds()),
			MetricName:               nodeReadyLatencyMeasurement,
			UUID:                     uuid,
			AMIID:                    amiID,
			JobName:                  JobName,
			Name:                     nodeMetricValue.Name,
			Labels:                   nodeMetricValue.Labels,
		})
	}
	quantileMap := map[string][]float64{}
	for _, normLatency := range normLatencies {
		quantileMap["MachineCreation"] = append(quantileMap["MachineCreation"], float64(normLatency.(NodeReadyMetric).MachineCreationLatency))
		quantileMap["MachineReady"] = append(quantileMap["MachineReady"], float64(normLatency.(NodeReadyMetric).MachineReadyLatency))
		quantileMap["NodeCreation"] = append(quantileMap["NodeCreation"], float64(normLatency.(NodeReadyMetric).NodeCreationLatency))
		quantileMap["NodeReady"] = append(quantileMap["NodeReady"], float64(normLatency.(NodeReadyMetric).NodeReadyLatency))
	}

	calcSummary := func(name string, latencies []float64) mmetrics.LatencyQuantiles {
		latencySummary := mmetrics.NewLatencySummary(latencies, name)
		latencySummary.UUID = uuid
		latencySummary.MetricName = nodeReadyLatencyQuantilesMeasurement
		latencySummary.JobName = JobName
		return latencySummary
	}

	for condition, latencies := range quantileMap {
		latencyQuantiles = append(latencyQuantiles, calcSummary(condition, latencies))
	}
	return normLatencies, latencyQuantiles
}
