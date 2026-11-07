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
	doneCh          chan struct{}
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
	s.doneCh = make(chan struct{})

	targetPod := selectImportPod(targets)
	if targetPod == "" {
		return nil
	}
	if err := s.createDummyInterfaces(); err != nil {
		return err
	}
	s.queueImportRoutes(targetPod)

	for range importWorkerCount {
		s.wg.Add(1)
		go s.worker()
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

// stop signals shutdown and waits for in-flight ping attempts to finish.
func (s *raImportScenario) stop() {
	close(s.doneCh)
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

// worker assigns addresses to generate routes that BGP advertises to the cluster.
// Pinging a CUDN pod from each new address verifies that its route was imported.
func (s *raImportScenario) worker() {
	defer s.wg.Done()
	for {
		select {
		case mc, ok := <-s.routeImportChan:
			if !ok {
				return
			}
			// Get the netlink.Link object for the given interface name
			link, err := netlink.LinkByName(mc.link)
			if err != nil {
				log.Errorf("Failed to get link %s: %v", mc.link, err)
			}
			// Parse IP address
			addr, err := netlink.ParseAddr(mc.addr)
			if err != nil {
				log.Errorf("Failed to parse IP address: %v", err)
			}

			importTimestamp := time.Now().UTC()
			// Generate route by adding IP address to the interface
			if err := netlink.AddrAdd(link, addr); err != nil {
				log.Errorf("Failed to add IP address: %v", err)
			}
			ipAddr, _, err := net.ParseCIDR(mc.addr)
			if err != nil {
				log.Errorf("Failed to add IP address: %v", err)
			}
			importLatency, err := pingOneCudn(mc.pod, ipAddr.String())
			if err != nil {
				log.Errorf("Failed to verify import route %s: %v", mc.addr, err)
				s.errCh <- fmt.Errorf("verify import route %s: %w", mc.addr, err)
				return
			}
			log.Debugf("Import route %s ping latency: %v ms", mc.addr, importLatency)

			s.metrics.LoadOrStore(mc.addr, raMetric{
				Name:       mc.addr,
				Timestamp:  importTimestamp,
				MetricName: raLatencyMeasurement,
				UUID:       s.metricContext.uuid,
				Metadata:   s.metricContext.metadata,
				JobName:    s.metricContext.jobName,
				Scenario:   "ImportRoutes",
				Latency:    importLatency,
			})
			atomic.AddUint64(&s.verifiedRouteCount, 1)

		case <-s.doneCh:
			return
		}
	}
}

// pingOneCudn retries a ping from an external address to a CUDN pod. It returns
// elapsed milliseconds on success, or -1 and the last error on failure.
func pingOneCudn(destPod, srcAddr string) (float64, error) {
	importTimestamp := time.Now().UTC()
	var pingErr error
	for range pingAttempts {
		if pingErr = pingAddress(srcAddr, destPod, importPingerTimeoutMsec); pingErr == nil {
			return float64(time.Since(importTimestamp).Milliseconds()), nil
		}
		time.Sleep(importWaitBeforePingRetryMsec * time.Millisecond)
	}
	return -1, pingErr
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
