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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	azruntime "github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	armnetwork "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v5"
	log "github.com/sirupsen/logrus"
)

// AzureRouteServerProvider implements BCCRouteServerProvider for Azure Route Server.
// T1 comes from the Route Server's BGP learned routes. Route table presence comes from the
// effective routes of the BGP peer NICs, since Azure exposes no separate FIB state.
type AzureRouteServerProvider struct {
	bgpClient       *armnetwork.VirtualHubBgpConnectionsClient
	nicClient       *armnetwork.InterfacesClient
	routeServerName string
	routeServerRG   string
	peerConnections []string // BGP connection names, discovered once at startup
	peerNICs        []*arm.ResourceID

	mu      sync.Mutex
	ribSeen sightings // prefix -> BGP connection -> when its learned routes first contained the prefix
	nicSeen sightings // subnet -> peer NIC -> when its effective routes first contained the subnet
}

// NewAzureRouteServerProvider creates a new Azure Route Server provider and discovers the NICs of its BGP peers.
func NewAzureRouteServerProvider(subscriptionID, routeServerRG, routeServerName string) (*AzureRouteServerProvider, error) {
	if subscriptionID == "" || routeServerRG == "" || routeServerName == "" {
		return nil, fmt.Errorf("azure subscription ID, resource group and route server name are required")
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create Azure credentials: %w", err)
	}

	bgpClient, err := armnetwork.NewVirtualHubBgpConnectionsClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create Azure BGP connections client: %w", err)
	}

	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create Azure network interfaces client: %w", err)
	}

	ipConfigClient, err := armnetwork.NewVirtualHubIPConfigurationClient(subscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create Azure route server IP configuration client: %w", err)
	}

	p := &AzureRouteServerProvider{
		bgpClient:       bgpClient,
		nicClient:       nicClient,
		routeServerName: routeServerName,
		routeServerRG:   routeServerRG,
		ribSeen:         sightings{},
		nicSeen:         sightings{},
	}
	ctx := context.Background()
	peers, err := p.bgpPeers(ctx)
	if err != nil {
		return nil, err
	}
	peerIPs := map[string]bool{}
	for name, ip := range peers {
		p.peerConnections = append(p.peerConnections, name)
		peerIPs[ip] = true
	}
	if p.peerNICs, err = p.discoverPeerNICs(ctx, ipConfigClient, peerIPs); err != nil {
		return nil, err
	}
	nicsByRG := map[string][]string{}
	for _, nic := range p.peerNICs {
		nicsByRG[nic.ResourceGroupName] = append(nicsByRG[nic.ResourceGroupName], nic.Name)
	}
	for rg, names := range nicsByRG {
		slices.Sort(names)
		log.Infof("Using %d BGP peer NICs in resource group %s for route table detection: %s", len(names), rg, strings.Join(names, ", "))
	}

	return p, nil
}

// discoverPeerNICs maps each BGP peer IP of the Route Server to the NIC holding that private IP
// in the Route Server's VNet. Private IPs are only unique within a VNet.
func (p *AzureRouteServerProvider) discoverPeerNICs(ctx context.Context, ipConfigClient *armnetwork.VirtualHubIPConfigurationClient, peerIPs map[string]bool) ([]*arm.ResourceID, error) {
	vnet, err := p.routeServerVNet(ctx, ipConfigClient)
	if err != nil {
		return nil, err
	}
	nicsByIP, err := p.nicsByIP(ctx, vnet, peerIPs)
	if err != nil {
		return nil, err
	}

	var nics []*arm.ResourceID
	for ip := range peerIPs {
		ids := nicsByIP[ip]
		if len(ids) != 1 {
			return nil, fmt.Errorf("BGP peer IP %s must match exactly one NIC in VNet %s, found %d: %v", ip, vnet, len(ids), ids)
		}
		nic, err := arm.ParseResourceID(ids[0])
		if err != nil {
			return nil, fmt.Errorf("invalid NIC resource ID %q: %w", ids[0], err)
		}
		nics = append(nics, nic)
	}
	return nics, nil
}

// routeServerVNet returns the VNet the Route Server is deployed in, taken from its IP configuration subnet.
func (p *AzureRouteServerProvider) routeServerVNet(ctx context.Context, ipConfigClient *armnetwork.VirtualHubIPConfigurationClient) (string, error) {
	pager := ipConfigClient.NewListPager(p.routeServerRG, p.routeServerName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to read route server IP configuration: %w", err)
		}
		for _, ipConfig := range page.Value {
			if ipConfig != nil && ipConfig.Properties != nil && ipConfig.Properties.Subnet != nil && ipConfig.Properties.Subnet.ID != nil {
				return vnetOf(*ipConfig.Properties.Subnet.ID), nil
			}
		}
	}
	return "", fmt.Errorf("could not determine the VNet of route server %s", p.routeServerName)
}

// bgpPeers returns the Route Server's BGP connections, mapping connection name to peer IP.
func (p *AzureRouteServerProvider) bgpPeers(ctx context.Context) (map[string]string, error) {
	peers := map[string]string{}
	pager := p.bgpClient.NewListPager(p.routeServerRG, p.routeServerName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list BGP connections: %w", err)
		}
		for _, conn := range page.Value {
			if conn != nil && conn.Name != nil && conn.Properties != nil && conn.Properties.PeerIP != nil {
				peers[*conn.Name] = *conn.Properties.PeerIP
			}
		}
	}
	if len(peers) == 0 {
		return nil, fmt.Errorf("route server %s has no BGP peers", p.routeServerName)
	}
	return peers, nil
}

// nicsByIP returns the IDs of the NICs in vnet that hold one of the given private IPs, keyed by IP.
func (p *AzureRouteServerProvider) nicsByIP(ctx context.Context, vnet string, ips map[string]bool) (map[string][]string, error) {
	nics := map[string][]string{}
	pager := p.nicClient.NewListAllPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list network interfaces: %w", err)
		}
		for _, nic := range page.Value {
			if nic == nil || nic.ID == nil || nic.Properties == nil {
				continue
			}
			for _, ipConfig := range nic.Properties.IPConfigurations {
				if ipConfig == nil || ipConfig.Properties == nil || ipConfig.Properties.PrivateIPAddress == nil ||
					ipConfig.Properties.Subnet == nil || ipConfig.Properties.Subnet.ID == nil {
					continue
				}
				ip := *ipConfig.Properties.PrivateIPAddress
				if ips[ip] && vnetOf(*ipConfig.Properties.Subnet.ID) == vnet {
					nics[ip] = append(nics[ip], *nic.ID)
				}
			}
		}
	}
	return nics, nil
}

// vnetOf returns the lower-cased VNet resource ID of a subnet resource ID.
func vnetOf(subnetID string) string {
	id := strings.ToLower(subnetID)
	if i := strings.Index(id, "/subnets/"); i >= 0 {
		return id[:i]
	}
	return id
}

// GetRoutes returns the routes the Route Server currently learns from its BGP peers (T1/T1.5). All peers
// are queried in parallel and each is stamped when its own query returns a prefix: FirstSeen is the earliest
// peer sighting, AllSeen the latest first sighting once every peer has learned the prefix.
// If any peer query fails the round is reported as an error, since it is not a complete snapshot;
// sightings from the peers that answered are kept, so no first sighting is lost.
func (p *AzureRouteServerProvider) GetRoutes(ctx context.Context) ([]RouteInfo, error) {
	var (
		mu        sync.Mutex
		wg        sync.WaitGroup
		current   = map[string]bool{}
		durations []time.Duration
		firstErr  error
	)
	for _, peer := range p.peerConnections {
		wg.Go(func() {
			start := time.Now()
			prefixes, err := p.learnedRoutes(ctx, peer)
			at := time.Now().UTC()
			mu.Lock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			for prefix := range prefixes {
				current[prefix] = true
			}
			durations = append(durations, at.Sub(start))
			mu.Unlock()

			p.mu.Lock()
			defer p.mu.Unlock()
			for prefix := range prefixes {
				p.ribSeen.record(prefix, peer, at)
			}
		})
	}
	wg.Wait()
	logQueryDurations("Learned routes (T1/T1.5)", durations)
	if firstErr != nil {
		return nil, firstErr
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	routes := make([]RouteInfo, 0, len(current))
	for prefix := range current {
		first, all := p.ribSeen.span(prefix, len(p.peerConnections))
		routes = append(routes, RouteInfo{Prefix: prefix, FirstSeen: first, AllSeen: all})
	}
	return routes, nil
}

// logQueryDurations logs the shortest and longest query of a parallel round. Azure takes its snapshot
// somewhere within each query, so this bounds how late a detection can be stamped.
func logQueryDurations(what string, durations []time.Duration) {
	if len(durations) == 0 {
		return
	}
	log.Debugf("%s: %d queries took %v to %v", what, len(durations),
		slices.Min(durations).Round(time.Millisecond), slices.Max(durations).Round(time.Millisecond))
}

// pollEverySecond drives a long-running operation to completion, checking its status every second.
// PollUntilDone honors the server's Retry-After (often ~10s), which would delay route detection by that much.
func pollEverySecond[T any](ctx context.Context, poller *azruntime.Poller[T]) (T, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for !poller.Done() {
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-ticker.C:
		}
		if _, err := poller.Poll(ctx); err != nil {
			var zero T
			return zero, err
		}
	}
	return poller.Result(ctx)
}

// learnedRoutes returns the prefixes the Route Server learned from one BGP peer.
func (p *AzureRouteServerProvider) learnedRoutes(ctx context.Context, peer string) (map[string]bool, error) {
	poller, err := p.bgpClient.BeginListLearnedRoutes(ctx, p.routeServerRG, p.routeServerName, peer, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query learned routes from peer %s: %w", peer, err)
	}
	res, err := pollEverySecond(ctx, poller)
	if err != nil {
		return nil, fmt.Errorf("failed to poll learned routes from peer %s: %w", peer, err)
	}

	prefixes := map[string]bool{}
	// Value maps each virtual router instance to the routes it learned
	for _, instanceRoutes := range res.Value {
		for _, route := range instanceRoutes {
			if route != nil && route.Network != nil {
				prefixes[*route.Network] = true
			}
		}
	}
	return prefixes, nil
}

// CheckRouteTables queries the effective routes of all BGP peer NICs in parallel. Each NIC is stamped
// when its own query returns the subnet, so the result does not depend on the slowest NIC in a round.
// FirstSeen is the earliest NIC sighting; AllSeen is the latest first sighting once every NIC has the subnet.
func (p *AzureRouteServerProvider) CheckRouteTables(ctx context.Context, subnets []string) (map[string]RouteTableStatus, error) {
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		firstErr  error
		durations []time.Duration
	)
	for _, nic := range p.peerNICs {
		wg.Go(func() {
			start := time.Now()
			prefixes, err := p.effectiveRoutes(ctx, nic)
			mu.Lock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			at := time.Now().UTC()
			durations = append(durations, at.Sub(start))
			mu.Unlock()

			p.mu.Lock()
			defer p.mu.Unlock()
			for _, subnet := range subnets {
				if prefixes[subnet] {
					p.nicSeen.record(subnet, nic.Name, at)
				}
			}
		})
	}
	wg.Wait()
	logQueryDurations("Effective routes (T3/T3.5)", durations)
	// Sightings from NICs that answered are kept, so a failed round loses no timestamps
	if firstErr != nil {
		return nil, firstErr
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	found := make(map[string]RouteTableStatus, len(subnets))
	for _, subnet := range subnets {
		first, all := p.nicSeen.span(subnet, len(p.peerNICs))
		found[subnet] = RouteTableStatus{FirstSeen: first, AllSeen: all}
	}
	return found, nil
}

// effectiveRoutes returns the address prefixes in a NIC's effective route table.
func (p *AzureRouteServerProvider) effectiveRoutes(ctx context.Context, nic *arm.ResourceID) (map[string]bool, error) {
	poller, err := p.nicClient.BeginGetEffectiveRouteTable(ctx, nic.ResourceGroupName, nic.Name, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query effective routes of NIC %s: %w", nic.Name, err)
	}
	res, err := pollEverySecond(ctx, poller)
	if err != nil {
		return nil, fmt.Errorf("failed to poll effective routes of NIC %s: %w", nic.Name, err)
	}

	prefixes := map[string]bool{}
	for _, route := range res.Value {
		if route == nil {
			continue
		}
		for _, prefix := range route.AddressPrefix {
			if prefix != nil {
				prefixes[*prefix] = true
			}
		}
	}
	return prefixes, nil
}

// ReportsFIB is false: Azure exposes no Route Server FIB state, only the NICs' effective routes.
func (p *AzureRouteServerProvider) ReportsFIB() bool { return false }

// Reset forgets the learned route and NIC effective route sightings.
func (p *AzureRouteServerProvider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ribSeen = sightings{}
	p.nicSeen = sightings{}
}
