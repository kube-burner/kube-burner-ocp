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

package utils

import (
	"context"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
)

// RouteInfo represents a cloud-agnostic route in a Route Server's routing database.
type RouteInfo struct {
	Prefix string // CIDR block (e.g., "10.0.0.0/16")
	InFIB  bool   // Whether the route is active in the FIB (AWS only; Azure exposes no FIB state)

	// Per-BGP-peer sightings; zero when the provider does not report them.
	FirstSeen time.Time // first peer that reported the route
	AllSeen   time.Time // when the last peer first reported it, once every peer has
}

// BCCRouteServerProvider defines the interface for cloud-specific Route Server operations.
// Implementations exist for AWS Route Server and Azure Route Server.
type BCCRouteServerProvider interface {
	// GetRoutes retrieves all routes from the Route Server's routing database.
	GetRoutes(ctx context.Context) ([]RouteInfo, error)

	// CheckRouteTables checks which subnets are present in cloud-specific routing resources.
	// For AWS: the route tables of the provider's VPC. For Azure: the effective routes of every BGP peer NIC.
	CheckRouteTables(ctx context.Context, subnets []string) (map[string]RouteTableStatus, error)

	// ReportsFIB reports whether GetRoutes returns the Route Server's FIB state (T2) and route table
	// installation status (T3.5), as on AWS. Otherwise both come from CheckRouteTables, as on Azure.
	ReportsFIB() bool

	// Reset forgets every route sighting, so that a new measurement does not inherit first-seen times
	// recorded by earlier queries (e.g. connectivity validation). Discovered peers are kept.
	Reset()
}

// RouteTableStatus reports when a subnet was first found; zero times mean not found yet. AllSeen is set
// once every location that must receive the route has it (Azure: BGP peer NICs; AWS: route tables the
// route server installs into).
type RouteTableStatus struct {
	FirstSeen time.Time // first found in any route table
	AllSeen   time.Time // found in every route table checked (when the last one first had it)
}

// sightings records, per prefix, when each peer (BGP connection or NIC) first reported it.
type sightings map[string]map[string]time.Time

func (s sightings) record(prefix, peer string, at time.Time) {
	if s[prefix] == nil {
		s[prefix] = map[string]time.Time{}
	}
	if _, seen := s[prefix][peer]; !seen {
		s[prefix][peer] = at
	}
}

// span returns the earliest sighting of prefix and, once all total peers have reported it, the latest first sighting.
func (s sightings) span(prefix string, total int) (first, all time.Time) {
	for _, at := range s[prefix] {
		if first.IsZero() || at.Before(first) {
			first = at
		}
		if at.After(all) {
			all = at
		}
	}
	if total <= 0 || len(s[prefix]) < total {
		all = time.Time{}
	}
	return first, all
}

// ValidateConnectivity tests a Route Server provider's connectivity.
// cloud is the provider name (aws, azure) for logging purposes.
func ValidateConnectivity(cloud string, provider BCCRouteServerProvider) error {
	ctx := context.Background()
	routes, err := provider.GetRoutes(ctx)
	if err != nil {
		return fmt.Errorf("failed to query Route Server: %w", err)
	}
	log.Infof("%s Route Server reachable: %d routes", cloud, len(routes))

	if _, err := provider.CheckRouteTables(ctx, nil); err != nil {
		return fmt.Errorf("failed to query route tables: %w", err)
	}
	log.Infof("%s route tables reachable", cloud)
	return nil
}
