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
	"sync"
	"time"
)

const (
	raLatencyMeasurement          = "raLatencyMeasurement"
	raLatencyQuantilesMeasurement = "raLatencyQuantilesMeasurement"
)

type raMetric struct {
	Timestamp  time.Time `json:"timestamp"`
	MetricName string    `json:"metricName"`
	UUID       string    `json:"uuid"`
	JobName    string    `json:"jobName,omitempty"`
	// route advertisement name
	Name     string `json:"routeAdvertisementName"`
	Metadata any    `json:"metadata,omitempty"`
	// whether this metric represents route latency for import scenario or export scenario
	Scenario string `json:"scenario,omitempty"`
	// CUDN used to match export observations during normalization.
	cudn string
	// ms from RA creation to Accepted=True; -1 if never observed (export only)
	AcceptedLatency float64 `json:"acceptedLatency,omitempty"`
	// ping test latency in ms for a single target (one pod for export, one CUDN for import)
	Latency float64 `json:"latency,omitempty"`
	// netlink route detection latency in ms
	NetlinkRouteLatency float64 `json:"netlinkRouteLatency,omitempty"`
}

// raMetricContext supplies fixed job identity to the components recording metrics.
type raMetricContext struct {
	uuid     string
	jobName  string
	metadata map[string]any
}

// generateResults calculates normalized copies while preserving the live raw
// metrics and the job identity captured when each observation was recorded.
func generateResults(metrics *sync.Map, exportResults map[string]netlinkRoutes) []raMetric {
	var results []raMetric
	metrics.Range(func(_, value any) bool {
		m := value.(raMetric)
		if m.Scenario == "ExportRoutes" {
			m.Latency = -1
			m.NetlinkRouteLatency = -1
			if timestamps, exists := exportResults[m.cudn]; exists {
				if !timestamps.pingTimestamp.IsZero() {
					m.Latency = float64(timestamps.pingTimestamp.Sub(m.Timestamp).Milliseconds())
				}
				m.NetlinkRouteLatency = float64(timestamps.routeTimestamp.Sub(m.Timestamp).Milliseconds())
			}
		}
		results = append(results, m)
		return true
	})
	return results
}

func (r *raLatency) getLatency(normLatency any) map[string]float64 {
	m := normLatency.(raMetric)
	return map[string]float64{
		"AcceptedLatency":     m.AcceptedLatency,
		"Latency":             m.Latency,
		"NetlinkRouteLatency": m.NetlinkRouteLatency,
	}
}
