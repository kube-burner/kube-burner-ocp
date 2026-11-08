//go:build integration

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
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

func TestRaLatencyIntegration(t *testing.T) {
	if os.Getenv(raIntegrationChild) != "1" {
		runRAIntegrationChild(t)
		return
	}
	api := startRAIntegrationAPI(t)
	for _, tc := range []struct {
		name       string
		mode       string
		wantExport bool
		wantImport bool
		timeout    bool
	}{
		{name: "export", mode: "export", wantExport: true},
		{name: "import", mode: "import", wantImport: true},
		{name: "both", mode: "export,import", wantExport: true, wantImport: true},
		{name: "accepted-only", mode: "none"},
		{name: "export-timeout", mode: "export", wantExport: true, timeout: true},
		{name: "import-timeout", mode: "import", wantImport: true, timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each case owns a peer namespace and uses distinct API resources.
			// Package-level measurement configuration is restored by cleanup.
			network := newRAIntegrationNetwork(t)
			name := "ra-" + tc.name
			seedRAIntegrationTargets(t, api, name)
			if !tc.wantExport && tc.wantImport {
				network.addTargetRoute(t)
			}
			timeout := 5 * time.Second
			if tc.timeout {
				timeout = 2 * time.Second
			}
			r, stop := startRAIntegrationMeasurement(t, api, name, tc.mode, timeout)
			if len(r.cudnSubnet) != 1 || len(r.cudnSubnet[raTargetSubnet].pods) != 1 {
				t.Fatalf("unexpected discovery results: %#v", r.cudnSubnet)
			}
			createAcceptedRA(t, api, r, name)
			if tc.wantExport {
				// Initial subscription does not replay routes: install after Start.
				network.addTargetRoute(t)
			}
			stopped := stop()
			if tc.wantExport {
				raEventually(t, "export ping reaching the target", func() bool { return network.echoRequests(t) > 0 })
				if r.exportScenario.verifiedCount() != 0 {
					t.Fatal("export verified a target while echo replies were disabled")
				}
				if tc.wantImport {
					assertRANoDummyInterface(t) // Import must wait for export to finish.
				}
			} else if tc.wantImport {
				raEventually(t, "all import source addresses before any replies", func() bool {
					link, err := netlink.LinkByName("dummy0")
					if err != nil {
						return false
					}
					addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
					return err == nil && len(addresses) == raImportTargets
				})
				raEventually(t, "import pings reaching the target", func() bool { return network.echoRequests(t) >= raImportTargets })
				if r.importScenario.verifiedCount() != 0 {
					t.Fatal("import verified a target while echo replies were disabled")
				}
			}
			if (tc.wantExport || tc.wantImport) && !tc.timeout {
				select {
				case err := <-stopped:
					t.Fatalf("scenario stopped before targets could reply: %v", err)
				default:
				}
				network.answerPings(t, true)
			}
			select {
			case err := <-stopped:
				if tc.timeout {
					if err == nil || !strings.Contains(err.Error(), tc.mode+" scenario timed out") {
						t.Fatalf("expected %s timeout, got %v", tc.mode, err)
					}
				} else if err != nil {
					t.Fatalf("Stop: %v", err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("Stop did not cancel and join the scenario workers")
			}
			assertRANoDummyInterface(t)
			assertRANoPingSockets(t)
			if !r.observer.factory.K8s().V1().RouteAdvertisements().Informer().IsStopped() {
				t.Fatal("RA informer is still running after Stop")
			}
			if !tc.timeout {
				assertRAIntegrationMetrics(t, r, tc.wantExport, tc.wantImport)
			}
			if !tc.wantExport && !tc.wantImport && network.echoRequests(t) != 0 {
				t.Fatal("accepted-only measurement sent pings")
			}
		})
	}
}

func assertRANoDummyInterface(t *testing.T) {
	t.Helper()
	_, err := netlink.LinkByName("dummy0")
	var notFound netlink.LinkNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected dummy0 to be absent, got %v", err)
	}
}

func assertRANoPingSockets(t *testing.T) {
	t.Helper()
	// This namespace contains only the measurement and the TCP-based API server.
	// Both export echo sockets and import raw sockets must be closed after Stop.
	for _, path := range []string{"/proc/net/icmp", "/proc/net/raw"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(strings.Split(strings.TrimSpace(string(data)), "\n")) != 1 {
			t.Fatalf("ping sockets remain after Stop: %s:\n%s", path, data)
		}
	}
}

func assertRAIntegrationMetrics(t *testing.T, r *raLatency, wantExport, wantImport bool) {
	t.Helper()
	var exports, imports int
	for _, metric := range r.NormLatencies {
		m := metric.(raMetric)
		if m.UUID != r.Uuid || m.JobName != r.JobConfig.Name {
			t.Fatalf("metric has incorrect job identity: %#v", m)
		}
		switch m.Scenario {
		case "ExportRoutes":
			exports++
			// The observer initializes acceptance latency to -1 until it sees
			// Accepted=True. Zero is valid at the API timestamps' precision.
			// This checks the sentinel, not the exact acceptance-time calculation.
			if m.AcceptedLatency < 0 {
				t.Fatalf("acceptance was not recorded: %#v", m)
			}
			// generateResults matches the RA's CUDN to export worker timestamps,
			// then subtracts RA creation time from route detection and first reply.
			// Missing observations remain -1. Nonnegative values alone do not
			// prove that the correct CUDN was matched or the deltas are exact.
			if wantExport && (m.Latency < 0 || m.NetlinkRouteLatency < 0) {
				t.Fatalf("export timestamps were not joined with the RA metric: %#v", m)
			}
			// RA acceptance is still observed with export checks disabled, but
			// connectivity latencies must retain their -1 sentinels.
			if !wantExport && (m.Latency != -1 || m.NetlinkRouteLatency != -1) {
				t.Fatalf("disabled export produced connectivity latencies: %#v", m)
			}
		case "ImportRoutes":
			imports++
			if m.Latency < 0 {
				t.Fatalf("import reply latency was not recorded: %#v", m)
			}
		default:
			t.Fatalf("unexpected scenario: %#v", m)
		}
	}
	expectedImports := 0
	if wantImport {
		expectedImports = raImportTargets
	}
	if exports != 1 || imports != expectedImports {
		t.Fatalf("expected 1 RA and %d import metrics, got %d and %d", expectedImports, exports, imports)
	}
	if len(r.LatencyQuantiles) != 3 {
		t.Fatalf("expected quantiles for three latency fields, got %d", len(r.LatencyQuantiles))
	}
}
