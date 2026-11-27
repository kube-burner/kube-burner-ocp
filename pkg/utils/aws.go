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
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// AWSRouteServerProvider provides methods to interact with AWS VPC Route Server.
// It implements the BCCRouteServerProvider interface.
type AWSRouteServerProvider struct {
	client        *ec2.Client
	routeServerID string
	vpcID         string // VPC whose route tables are checked for T3
	peerCount     int    // available route server peers, counted once at startup

	mu            sync.Mutex
	ribSeen       sightings                  // prefix -> peer -> first poll with a routing database entry from that peer
	installSeen   sightings                  // prefix -> route table -> first poll reporting the route installed there
	installTables map[string]map[string]bool // prefix -> route tables the route server installs it into
}

// NewAWSRouteServerProvider creates a new AWS Route Server provider that implements BCCRouteServerProvider.
func NewAWSRouteServerProvider(region, routeServerID, vpcID string) (*AWSRouteServerProvider, error) {
	if region == "" {
		return nil, fmt.Errorf("AWS region is empty")
	}
	if routeServerID == "" {
		return nil, fmt.Errorf("AWS Route Server ID is empty")
	}
	if vpcID == "" {
		return nil, fmt.Errorf("AWS VPC ID is empty")
	}

	// Loads credentials, region, profile, IAM role, etc.
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("unable to load AWS config: %w", err)
	}

	p := &AWSRouteServerProvider{
		client:        ec2.NewFromConfig(cfg),
		routeServerID: routeServerID,
		vpcID:         vpcID,
		ribSeen:       sightings{},
		installSeen:   sightings{},
		installTables: map[string]map[string]bool{},
	}
	if p.peerCount, err = p.countPeers(context.Background()); err != nil {
		return nil, err
	}
	return p, nil
}

// countPeers returns the number of available peers of the route server.
func (p *AWSRouteServerProvider) countPeers(ctx context.Context) (int, error) {
	count := 0
	var nextToken *string
	for {
		output, err := p.client.DescribeRouteServerPeers(ctx, &ec2.DescribeRouteServerPeersInput{NextToken: nextToken})
		if err != nil {
			return 0, fmt.Errorf("describe route server peers: %w", err)
		}
		for _, peer := range output.RouteServerPeers {
			if aws.ToString(peer.RouteServerId) == p.routeServerID && peer.State == ec2types.RouteServerPeerStateAvailable {
				count++
			}
		}
		if aws.ToString(output.NextToken) == "" {
			break
		}
		nextToken = output.NextToken
	}
	if count == 0 {
		return 0, fmt.Errorf("route server %s has no available peers", p.routeServerID)
	}
	return count, nil
}

// GetRoutes retrieves the AWS Route Server routing database, which has one entry per prefix and peer, and returns
// one RouteInfo per prefix. FirstSeen/AllSeen record the first poll with an entry from any/every peer (T1/T1.5).
// The entries' route table installation status is kept for CheckRouteTables (T3.5).
func (p *AWSRouteServerProvider) GetRoutes(ctx context.Context) ([]RouteInfo, error) {
	var awsRoutes []ec2types.RouteServerRoute
	var nextToken *string

	for {
		output, err := p.client.GetRouteServerRoutingDatabase(ctx, &ec2.GetRouteServerRoutingDatabaseInput{
			RouteServerId: aws.String(p.routeServerID),
			MaxResults:    aws.Int32(1000),
			NextToken:     nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("get route server routing database: %w", err)
		}
		awsRoutes = append(awsRoutes, output.Routes...)
		if aws.ToString(output.NextToken) == "" {
			break
		}
		nextToken = output.NextToken
	}

	now := time.Now().UTC()
	p.mu.Lock()
	defer p.mu.Unlock()

	inFIB := map[string]bool{}
	for _, route := range awsRoutes {
		prefix := aws.ToString(route.Prefix)
		inFIB[prefix] = inFIB[prefix] || route.RouteStatus == ec2types.RouteServerRouteStatusInFib
		p.ribSeen.record(prefix, aws.ToString(route.RouteServerPeerId), now)
		for _, detail := range route.RouteInstallationDetails {
			table := aws.ToString(detail.RouteTableId)
			if p.installTables[prefix] == nil {
				p.installTables[prefix] = map[string]bool{}
			}
			p.installTables[prefix][table] = true
			if detail.RouteInstallationStatus == ec2types.RouteServerRouteInstallationStatusInstalled {
				p.installSeen.record(prefix, table, now)
			}
		}
	}

	routes := make([]RouteInfo, 0, len(inFIB))
	for prefix, fib := range inFIB {
		first, all := p.ribSeen.span(prefix, p.peerCount)
		routes = append(routes, RouteInfo{Prefix: prefix, InFIB: fib, FirstSeen: first, AllSeen: all})
	}
	return routes, nil
}

// CheckRouteTables checks which subnets are present in the VPC's route tables.
func (p *AWSRouteServerProvider) CheckRouteTables(ctx context.Context, subnets []string) (map[string]RouteTableStatus, error) {
	output, err := p.client.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{p.vpcID}}},
	})
	if err != nil {
		return nil, fmt.Errorf("describe route tables: %w", err)
	}
	now := time.Now().UTC()

	found := make(map[string]RouteTableStatus, len(subnets))
	for _, subnet := range subnets {
		found[subnet] = RouteTableStatus{}
	}
	for _, rt := range output.RouteTables {
		for _, route := range rt.Routes {
			destCidr := aws.ToString(route.DestinationCidrBlock)
			if _, checking := found[destCidr]; checking {
				found[destCidr] = RouteTableStatus{FirstSeen: now}
			}
		}
	}

	// T3.5: every route table the route server installs the route into reports it installed,
	// as recorded from the routing database by GetRoutes
	p.mu.Lock()
	defer p.mu.Unlock()
	for subnet, status := range found {
		_, status.AllSeen = p.installSeen.span(subnet, len(p.installTables[subnet]))
		found[subnet] = status
	}

	return found, nil
}

// ReportsFIB is true: the routing database has each route's FIB and installation status.
func (p *AWSRouteServerProvider) ReportsFIB() bool { return true }

// Reset forgets the routing database and installation sightings.
func (p *AWSRouteServerProvider) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ribSeen = sightings{}
	p.installSeen = sightings{}
	p.installTables = map[string]map[string]bool{}
}
