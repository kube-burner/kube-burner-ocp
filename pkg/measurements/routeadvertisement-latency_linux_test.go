// Copyright 2026 The Kube-burner Authors.
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
	"testing"
	"time"
)

func TestNormalizeGroupedRAExportLatency(t *testing.T) {
	start := time.Unix(1, 0)
	first := netlinkRoutes{
		routeTimestamp: start.Add(50 * time.Millisecond),
		pingTimestamp:  start.Add(100 * time.Millisecond),
	}
	lastRoute := netlinkRoutes{
		routeTimestamp: start.Add(80 * time.Millisecond),
		pingTimestamp:  start.Add(150 * time.Millisecond),
	}
	lastPing := netlinkRoutes{
		routeTimestamp: start.Add(60 * time.Millisecond),
		pingTimestamp:  start.Add(250 * time.Millisecond),
	}
	for _, tc := range []struct {
		name        string
		cudns       []string
		routes      map[string]netlinkRoutes
		wantLatency float64
		wantNetlink float64
	}{
		{
			name: "single", cudns: []string{"cudn-0"},
			routes:      map[string]netlinkRoutes{"cudn-0": first},
			wantLatency: 100, wantNetlink: 50,
		},
		{
			name: "grouped", cudns: []string{"cudn-0", "cudn-1", "cudn-2"},
			routes:      map[string]netlinkRoutes{"cudn-0": first, "cudn-1": lastRoute, "cudn-2": lastPing},
			wantLatency: 250, wantNetlink: 80,
		},
		{
			name: "missing route", cudns: []string{"cudn-0", "cudn-1"},
			routes:      map[string]netlinkRoutes{"cudn-0": first},
			wantLatency: -1, wantNetlink: -1,
		},
		{
			name: "missing ping", cudns: []string{"cudn-0", "cudn-1"},
			routes: map[string]netlinkRoutes{
				"cudn-0": first, "cudn-1": {routeTimestamp: lastRoute.routeTimestamp},
			},
			wantLatency: -1, wantNetlink: 80,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &raLatency{}
			r.Metrics.Store("ra-0", raMetric{
				Name: "ra-0", Timestamp: start, Scenario: "ExportRoutes",
				cudns: tc.cudns, AcceptedLatency: 25,
			})
			for name, route := range tc.routes {
				r.cudnConnTimestamp.Store(name, route)
			}
			r.normalizeMetrics()
			if len(r.NormLatencies) != 1 {
				t.Fatalf("expected one metric per RA, got %d", len(r.NormLatencies))
			}
			got := r.NormLatencies[0].(raMetric)
			if got.Latency != tc.wantLatency || got.NetlinkRouteLatency != tc.wantNetlink {
				t.Fatalf("got ping/route latency %v/%v ms, want %v/%v ms", got.Latency, got.NetlinkRouteLatency, tc.wantLatency, tc.wantNetlink)
			}
			if got.AcceptedLatency != 25 {
				t.Fatalf("expected RA acceptance latency to stay 25 ms, got %v", got.AcceptedLatency)
			}
		})
	}
}
