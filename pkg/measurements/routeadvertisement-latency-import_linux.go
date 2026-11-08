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

const (
	importWorkerCount             = 10
	importWaitBeforePingRetryMsec = 100
	importPingerTimeoutMsec       = 10
)

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
// It closes the populated queue so workers exit once all tasks are consumed.
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
func (s *raImportScenario) stop() {
	s.cancel()
	s.wg.Wait()
}

// cleanup removes the dummy interfaces after workers have exited.
func (s *raImportScenario) cleanup() {
	for i := range s.numDummyIfaces {
		if err := s.deleteDummyInterface(i); err != nil {
			log.Errorf("Error deleting dummy interface %d: %v", i, err)
		}
	}
}

// worker consumes import tasks until shutdown or a verification failure.
func (s *raImportScenario) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case route, ok := <-s.routeImportChan:
			if !ok {
				return
			}
			if err := s.processImportRoute(ctx, route); err != nil {
				if ctx.Err() == nil {
					s.errCh <- err
				}
				return
			}

		case <-ctx.Done():
			return
		}
	}
}

// processImportRoute generates an external route by assigning its source address,
// then verifies connectivity to the target pod and records the result.
// Setup errors are logged and skipped; verification errors are returned.
func (s *raImportScenario) processImportRoute(ctx context.Context, route routeImport) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	link, err := netlink.LinkByName(route.link)
	if err != nil {
		log.Errorf("Skipping import route %s: failed to get link %s: %v", route.addr, route.link, err)
		return nil
	}
	addr, err := netlink.ParseAddr(route.addr)
	if err != nil {
		log.Errorf("Skipping import route %s: failed to parse IP address: %v", route.addr, err)
		return nil
	}

	importTimestamp := time.Now().UTC()
	if err := netlink.AddrAdd(link, addr); err != nil {
		log.Errorf("Skipping import route %s: failed to add IP address to %s: %v", route.addr, route.link, err)
		return nil
	}
	sourceIP, _, err := net.ParseCIDR(route.addr)
	if err != nil {
		log.Errorf("Skipping import route %s: failed to parse source CIDR: %v", route.addr, err)
		return nil
	}
	latency, err := pingPod(ctx, route.pod, sourceIP.String())
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Errorf("Failed to verify import route %s: %v", route.addr, err)
		return fmt.Errorf("verify import route %s: %w", route.addr, err)
	}
	log.Debugf("Import route %s ping latency: %v ms", route.addr, latency)
	s.recordImportResult(route, importTimestamp, latency)
	return nil
}

func (s *raImportScenario) recordImportResult(route routeImport, startedAt time.Time, latency float64) {
	s.metrics.LoadOrStore(route.addr, raMetric{
		Name:       route.addr,
		Timestamp:  startedAt,
		MetricName: raLatencyMeasurement,
		UUID:       s.metricContext.uuid,
		Metadata:   s.metricContext.metadata,
		JobName:    s.metricContext.jobName,
		Scenario:   "ImportRoutes",
		Latency:    latency,
	})
	atomic.AddUint64(&s.verifiedRouteCount, 1)
}

// pingPod retries until the pod replies or the scenario cancels its context.
// It returns elapsed milliseconds on success, or -1 and the cancellation error.
func pingPod(ctx context.Context, targetPod, sourceIP string) (float64, error) {
	importTimestamp := time.Now().UTC()
	for {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		if err := pingAddress(ctx, sourceIP, targetPod, importPingerTimeoutMsec); err == nil {
			return float64(time.Since(importTimestamp).Milliseconds()), nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(importWaitBeforePingRetryMsec * time.Millisecond):
		}
	}
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
