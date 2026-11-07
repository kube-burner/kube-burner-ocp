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
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

const (
	exportWorkerCount             = 20
	exportWaitBeforePingRetryMsec = 100
	exportPingerTimeoutMsec       = 100
)

var exportScenarioMaxTimeout time.Duration = time.Minute

// raExportScenario observes exported kernel routes and verifies pod connectivity.
type raExportScenario struct {
	cudnSubnet map[string]cudnPods
	errCh      chan error

	subscription *raRouteSubscription
	doneCh       chan struct{}
	wg           sync.WaitGroup

	verifiedRouteCount uint64
	verifiedRoutes     sync.Map // subnet -> struct{}
	cudnConnTimestamp  sync.Map // CUDN name -> netlinkRoutes
}

// netlinkRoutes holds the route detection and successful pod ping timestamps.
type netlinkRoutes struct {
	routeTimestamp time.Time
	// Zero means the ping has not succeeded.
	pingTimestamp time.Time
}

// start subscribes to kernel route updates before starting workers.
func (s *raExportScenario) start(targets map[string]cudnPods) error {
	s.cudnSubnet = targets
	s.errCh = make(chan error, exportWorkerCount)
	s.doneCh = make(chan struct{})

	subscription, err := subscribeRARoutes()
	if err != nil {
		log.Errorf("Failed to subscribe to route updates: %v", err)
		return err
	}
	s.subscription = subscription

	// Start export worker goroutines
	for range exportWorkerCount {
		s.wg.Add(1)
		go s.worker()
	}
	return nil
}

func (s *raExportScenario) verifiedCount() uint64 {
	return atomic.LoadUint64(&s.verifiedRouteCount)
}

func (s *raExportScenario) errors() <-chan error {
	return s.errCh
}

// stop signals workers and stops the subscription before waiting for workers.
func (s *raExportScenario) stop() {
	close(s.doneCh)
	s.subscription.stop()
	s.wg.Wait()
}

// results returns route and ping timestamps after workers have stopped.
func (s *raExportScenario) results() map[string]netlinkRoutes {
	results := make(map[string]netlinkRoutes)
	s.cudnConnTimestamp.Range(func(key, value any) bool {
		results[key.(string)] = value.(netlinkRoutes)
		return true
	})
	return results
}

// worker matches kernel route updates to discovered CUDNs and pings a pod to
// verify connectivity. Only one worker claims each CUDN.
func (s *raExportScenario) worker() {
	defer s.wg.Done()
	for {
		select {
		case update, ok := <-s.subscription.updates():
			if !ok {
				return
			}
			if update.Type == unix.RTM_NEWROUTE {
				cudnpods, exists := s.cudnSubnet[update.Dst.String()]
				if exists {
					log.Debugf("Netlink route: %s received for udn: %s at: %v", update.Dst.String(), cudnpods.cudn, time.Now().UTC())
					val, loaded := s.cudnConnTimestamp.LoadOrStore(cudnpods.cudn, netlinkRoutes{
						routeTimestamp: time.Now().UTC(),
					})
					if loaded {
						// Another event already claimed this CUDN; keep serving other CUDNs.
						continue
					}
					if nlRouteVal, ok := val.(netlinkRoutes); ok {
						if nlRouteVal.pingTimestamp.IsZero() {
							if len(cudnpods.pods) == 0 {
								s.errCh <- fmt.Errorf("verify export route %s for CUDN %s: no pods available", update.Dst.String(), cudnpods.cudn)
								return
							}
							pod := cudnpods.pods[0]
							var pingErr error
							for range pingAttempts {
								pingErr = pingAddress("", pod, exportPingerTimeoutMsec)
								if pingErr == nil {
									log.Debugf("Ping success to pod %s for the Netlink route: %s received for udn: %s at: %v", pod, update.Dst.String(), cudnpods.cudn, time.Now().UTC())
									nlRouteVal.pingTimestamp = time.Now().UTC()
									break
								}
								time.Sleep(exportWaitBeforePingRetryMsec * time.Millisecond)
							}
							if nlRouteVal.pingTimestamp.IsZero() {
								s.errCh <- fmt.Errorf("verify export route %s for CUDN %s: %w", update.Dst.String(), cudnpods.cudn, pingErr)
								return
							}
						}
						if !nlRouteVal.pingTimestamp.IsZero() {
							s.cudnConnTimestamp.Store(cudnpods.cudn, nlRouteVal)
							if _, loaded := s.verifiedRoutes.LoadOrStore(update.Dst.String(), struct{}{}); !loaded {
								atomic.AddUint64(&s.verifiedRouteCount, 1)
							}
						}
					}
				}
			}
		case <-s.doneCh:
			return
		}
	}
}
