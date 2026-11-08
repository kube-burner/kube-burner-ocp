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
	"context"
	"fmt"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

const importWorkerCount = 10

var (
	// Job input defaults; generated route count is the product of these values.
	numDummyIfaces                         = 20
	numAddressOnDummyIface                 = 10
	importRoutesCount                      = numDummyIfaces * numAddressOnDummyIface
	importScenarioMaxTimeout time.Duration = time.Minute
)

// raImportScenario generates external routes and verifies their import into the cluster.
type raImportScenario struct {
	numDummyIfaces         int
	numAddressOnDummyIface int
	expectedRouteCount     int
	metrics                *sync.Map
	metricContext          raMetricContext
	errCh                  chan error

	routeImportChan chan routeImport
	cancel          context.CancelFunc
	wg              sync.WaitGroup

	verifiedRouteCount uint64
}

// routeImport describes an address to assign to a dummy interface and the CUDN
// pod to ping using that address as the source. Workers consume these tasks.
type routeImport struct {
	link string
	addr string
	pod  string
}

// start selects a CUDN pod, creates dummy interfaces, and queues
// one unique source CIDR per interface/address pair before starting workers.
// It closes the populated queue; workers keep pending pingers alive until shutdown.
// If no target pod is available, it returns without starting workers.
func (s *raImportScenario) start(targets map[string]cudnPods) error {
	s.errCh = make(chan error, importWorkerCount)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	targetPod := selectImportPod(targets)
	if targetPod == "" {
		cancel()
		return nil
	}
	if err := s.createDummyInterfaces(); err != nil {
		cancel()
		return err
	}
	s.queueImportRoutes(targetPod)

	for range importWorkerCount {
		s.wg.Add(1)
		go s.worker(ctx)
	}
	return nil
}

func selectImportPod(targets map[string]cudnPods) string {
	// Choose a target by sorted CUDN name. If a CUDN spans multiple subnets,
	// subnetByCudn retains one subnet from map iteration.
	cudnNames := make([]string, 0, len(targets))
	subnetByCudn := make(map[string]string, len(targets))
	for subnet, cpods := range targets {
		cudnNames = append(cudnNames, cpods.cudn)
		subnetByCudn[cpods.cudn] = subnet
	}
	slices.Sort(cudnNames)
	for _, cudnName := range cudnNames {
		subnet := subnetByCudn[cudnName]
		cpods := targets[subnet]
		if len(cpods.pods) > 0 {
			return cpods.pods[0]
		}
	}
	return ""
}

func (s *raImportScenario) createDummyInterfaces() error {
	for interfaceIndex := range s.numDummyIfaces {
		if err := s.createDummyInterface(interfaceIndex); err != nil {
			return err
		}
	}
	return nil
}

// queueImportRoutes fills and closes the buffered queue before workers start.
func (s *raImportScenario) queueImportRoutes(targetPod string) {
	routeCount := s.numAddressOnDummyIface * s.numDummyIfaces
	s.expectedRouteCount = routeCount
	s.routeImportChan = make(chan routeImport, routeCount)
	for addressIndex := range s.numAddressOnDummyIface {
		for interfaceIndex := range s.numDummyIfaces {
			route := routeImport{
				link: fmt.Sprintf("dummy%d", interfaceIndex),
				addr: fmt.Sprintf("20.%d.%d.1/24", interfaceIndex, addressIndex+1),
				pod:  targetPod,
			}
			s.routeImportChan <- route
		}
	}
	close(s.routeImportChan)
}

func (s *raImportScenario) verifiedCount() uint64 {
	return atomic.LoadUint64(&s.verifiedRouteCount)
}

func (s *raImportScenario) expectedCount() uint64 {
	return uint64(s.expectedRouteCount)
}

func (s *raImportScenario) errors() <-chan error {
	return s.errCh
}

// stop cancels active pings and retry delays, then waits for workers to exit.
func (s *raImportScenario) stop() error {
	s.cancel()
	s.wg.Wait()
	// Completion may win the coordinator's select while a fatal error is queued.
	var firstErr error
	for {
		select {
		case err := <-s.errCh:
			if firstErr == nil {
				firstErr = err
				continue
			}
			log.Debugf("Additional import scenario error: %v", err)
		default:
			return firstErr
		}
	}
}

// cleanup removes the dummy interfaces after workers have exited.
func (s *raImportScenario) cleanup() {
	for i := range s.numDummyIfaces {
		if err := s.deleteDummyInterface(i); err != nil {
			log.Errorf("Error deleting dummy interface %d: %v", i, err)
		}
	}
}

// worker configures routes and owns independent pingers until scenario shutdown.
func (s *raImportScenario) worker(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	var pingers sync.WaitGroup
	// Each queued route is consumed once and emits at most two results. Capacity
	// for both allows shutdown to join all pingers before draining their results.
	results := make(chan raImportPingResult, raPingEventsPerTarget*s.expectedRouteCount)
	var workerErr error
	defer func() {
		cancel()
		pingers.Wait()
		close(results)
		for result := range results {
			if result.err != nil {
				if workerErr == nil {
					workerErr = result.err
				}
				continue
			}
			s.recordImportResult(result)
		}
		if workerErr != nil {
			// At most one error per worker fits the scenario's error buffer.
			s.errCh <- workerErr
		}
		s.wg.Done()
	}()

	routes := s.routeImportChan
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case route, ok := <-routes:
			if !ok {
				// Setup is finished; pending pingers still need results or timeout.
				routes = nil
				continue
			}
			target, err := prepareImportRoute(ctx, route)
			if err != nil {
				if ctx.Err() == nil {
					workerErr = err
				}
				return
			}
			if target == nil {
				continue
			}
			pingers.Add(1)
			go func(target raImportPingTarget) {
				defer pingers.Done()
				runImportPinger(ctx, target, results)
			}(*target)
		case result := <-results:
			if result.err != nil {
				workerErr = result.err
				return
			}
			s.recordImportResult(result)
		case <-ctx.Done():
			return
		}
	}
}

// prepareImportRoute assigns an external source address and returns its ping target.
// Setup errors are logged and skipped so workers can configure remaining routes.
func prepareImportRoute(ctx context.Context, route routeImport) (*raImportPingTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	link, err := netlink.LinkByName(route.link)
	if err != nil {
		log.Errorf("Skipping import route %s: failed to get link %s: %v", route.addr, route.link, err)
		return nil, nil
	}
	addr, err := netlink.ParseAddr(route.addr)
	if err != nil {
		log.Errorf("Skipping import route %s: failed to parse IP address: %v", route.addr, err)
		return nil, nil
	}

	importTimestamp := time.Now().UTC()
	if err := netlink.AddrAdd(link, addr); err != nil {
		log.Errorf("Skipping import route %s: failed to add IP address to %s: %v", route.addr, route.link, err)
		return nil, nil
	}
	sourceIP, _, err := net.ParseCIDR(route.addr)
	if err != nil {
		log.Errorf("Skipping import route %s: failed to parse source CIDR: %v", route.addr, err)
		return nil, nil
	}
	return &raImportPingTarget{
		route:          route,
		sourceIP:       sourceIP.String(),
		routeTimestamp: importTimestamp,
	}, nil
}

func (s *raImportScenario) recordImportResult(result raImportPingResult) {
	route := result.target.route
	log.Debugf("Import route %s ping latency: %v ms", route.addr, result.latency)
	s.metrics.LoadOrStore(route.addr, raMetric{
		Name:       route.addr,
		Timestamp:  result.target.routeTimestamp,
		MetricName: raLatencyMeasurement,
		UUID:       s.metricContext.uuid,
		Metadata:   s.metricContext.metadata,
		JobName:    s.metricContext.jobName,
		Scenario:   "ImportRoutes",
		Latency:    result.latency,
	})
	atomic.AddUint64(&s.verifiedRouteCount, 1)
}

// deleteDummyInterface removes an import interface and its assigned addresses/routes.
func (s *raImportScenario) deleteDummyInterface(i int) error {
	var err error
	ifaceName := fmt.Sprintf("dummy%d", i)
	link, err := netlink.LinkByName(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to find interface %s: %v", ifaceName, err)
	}

	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("failed to delete interface %s: %v", ifaceName, err)
	}
	return err
}

// createDummyInterface creates and brings up dummy<i> for import verification.
// Routes are generated later, when import workers assign addresses to it.
func (s *raImportScenario) createDummyInterface(i int) error {
	var err error
	// Define the name of the dummy interface
	ifaceName := fmt.Sprintf("dummy%d", i)

	// Create a dummy link (interface)
	dummy := &netlink.Dummy{
		LinkAttrs: netlink.LinkAttrs{
			Name: ifaceName,
		},
	}

	// Add the dummy interface
	if err := netlink.LinkAdd(dummy); err != nil {
		log.Errorf("Failed to add dummy interface: %v", err)
		return err
	}

	// Bring the interface up
	if err := netlink.LinkSetUp(dummy); err != nil {
		log.Errorf("Failed to bring up interface: %v", err)
		return err
	}

	// Verify interface exists
	_, err = netlink.LinkByName(ifaceName)
	if err != nil {
		log.Errorf("Failed to get interface: %v", err)
	}
	return err
}
