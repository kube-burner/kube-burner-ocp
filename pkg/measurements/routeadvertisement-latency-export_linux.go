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
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const exportWorkerCount = 20

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
func (s *raExportScenario) stop() error {
	close(s.doneCh)
	s.subscription.stop()
	s.wg.Wait()
	// A pinger can fail while the coordinator is observing the final success.
	// Keep that error visible even if completion won the coordinator's select.
	select {
	case err := <-s.errCh:
		return err
	default:
		return nil
	}
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

// worker owns its pingers and joins them before signaling scenario completion.
func (s *raExportScenario) worker() {
	ctx, cancel := context.WithCancel(context.Background())
	var pingers sync.WaitGroup
	// Each claimed CUDN emits at most a first reply and a terminal error. Keeping
	// room for both lets shutdown join producers before draining pending results.
	results := make(chan raExportPingResult, exportPingEventsPerTarget*len(s.cudnSubnet))
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
			s.recordExportResult(result)
		}
		if workerErr != nil {
			// One error per worker keeps the scenario error channel bounded.
			s.errCh <- workerErr
		}
		s.wg.Done()
	}()

	for {
		select {
		case <-s.doneCh:
			return
		default:
		}
		select {
		case update, ok := <-s.subscription.updates():
			if !ok {
				return
			}
			select {
			case <-s.doneCh:
				return
			default:
			}
			target, err := s.claimRouteUpdate(update)
			if err != nil {
				workerErr = err
				return
			}
			if target == nil {
				continue
			}
			pingers.Add(1)
			go func(target raExportPingTarget) {
				defer pingers.Done()
				runExportPinger(ctx, target, results)
			}(*target)
		case result := <-results:
			if result.err != nil {
				workerErr = result.err
				return
			}
			s.recordExportResult(result)
		case <-s.doneCh:
			return
		}
	}
}

// claimRouteUpdate claims a CUDN before pinging so duplicate updates cannot
// trigger concurrent verification of the same CUDN.
func (s *raExportScenario) claimRouteUpdate(update netlink.RouteUpdate) (*raExportPingTarget, error) {
	if update.Type != unix.RTM_NEWROUTE {
		return nil, nil
	}
	subnet := update.Dst.String()
	cudn, exists := s.cudnSubnet[subnet]
	if !exists {
		return nil, nil
	}
	log.Debugf("Netlink route: %s received for udn: %s at: %v", subnet, cudn.cudn, time.Now().UTC())
	state := netlinkRoutes{routeTimestamp: time.Now().UTC()}
	if _, loaded := s.cudnConnTimestamp.LoadOrStore(cudn.cudn, state); loaded {
		return nil, nil
	}
	if len(cudn.pods) == 0 {
		return nil, fmt.Errorf("verify export route %s for CUDN %s: no pods available", subnet, cudn.cudn)
	}
	return &raExportPingTarget{
		cudn:           cudn.cudn,
		subnet:         subnet,
		pod:            cudn.pods[0],
		routeTimestamp: state.routeTimestamp,
	}, nil
}

func (s *raExportScenario) recordExportResult(result raExportPingResult) {
	target := result.target
	log.Debugf("Ping success to pod %s for the Netlink route: %s received for udn: %s at: %v", target.pod, target.subnet, target.cudn, result.firstReply)
	s.cudnConnTimestamp.Store(target.cudn, netlinkRoutes{
		routeTimestamp: target.routeTimestamp,
		pingTimestamp:  result.firstReply,
	})
	if _, loaded := s.verifiedRoutes.LoadOrStore(target.subnet, struct{}{}); !loaded {
		atomic.AddUint64(&s.verifiedRouteCount, 1)
	}
}
