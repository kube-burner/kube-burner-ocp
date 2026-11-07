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
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

const raRouteSubscribeRetryInterval = time.Second

// raRouteSubscription owns the reconnect loop and a stable update queue for
// consumers, independently of each underlying socket connection.
type raRouteSubscription struct {
	routeCh   chan netlink.RouteUpdate
	doneCh    chan struct{}
	stoppedCh chan struct{}
}

// raRouteConnection owns the channels for one netlink socket.
type raRouteConnection struct {
	updates chan netlink.RouteUpdate
	doneCh  chan struct{}
	errCh   chan error
}

func subscribeRARoutes() (*raRouteSubscription, error) {
	connection, err := subscribeRARouteConnection(false)
	if err != nil {
		return nil, err
	}
	subscription := &raRouteSubscription{
		routeCh:   make(chan netlink.RouteUpdate, 10000),
		doneCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
	}
	go subscription.monitorRoutes(connection)
	return subscription, nil
}

func (s *raRouteSubscription) updates() <-chan netlink.RouteUpdate {
	return s.routeCh
}

func (s *raRouteSubscription) stop() {
	close(s.doneCh)
	<-s.stoppedCh
}

func subscribeRARouteConnection(listExisting bool) (*raRouteConnection, error) {
	connection := &raRouteConnection{
		updates: make(chan netlink.RouteUpdate),
		doneCh:  make(chan struct{}),
		errCh:   make(chan error, 1),
	}
	err := netlink.RouteSubscribeWithOptions(connection.updates, connection.doneCh, netlink.RouteSubscribeOptions{
		ListExisting: listExisting,
		ErrorCallback: func(err error) {
			select {
			case <-connection.doneCh:
				// Closing the socket during shutdown also produces a receive error.
				return
			default:
			}
			select {
			case connection.errCh <- err:
			default:
				// One notification is enough to trigger reconnection and a rescan.
			}
		},
	})
	if err != nil {
		close(connection.doneCh)
		return nil, err
	}
	return connection, nil
}

func (connection *raRouteConnection) stop() {
	close(connection.doneCh)
	// Netlink sends without selecting on doneCh. Drain until its receive
	// goroutine exits so a blocked send cannot leak the subscription.
	for range connection.updates {
	}
}

func (s *raRouteSubscription) monitorRoutes(connection *raRouteConnection) {
	defer close(s.stoppedCh)
	defer close(s.routeCh)
	for {
		select {
		case <-s.doneCh:
			connection.stop()
			return
		case err := <-connection.errCh:
			log.Warnf("Netlink route subscription error: %v; reconnecting and rescanning routes", err)
		case update, open := <-connection.updates:
			if open {
				select {
				case s.routeCh <- update:
				case <-s.doneCh:
					connection.stop()
					return
				}
				continue
			}
			log.Warn("Netlink route subscription closed; reconnecting and rescanning routes")
		}
		connection.stop()
		connection = s.reconnectRoutes()
		if connection == nil {
			return
		}
	}
}

func (s *raRouteSubscription) reconnectRoutes() *raRouteConnection {
	ticker := time.NewTicker(raRouteSubscribeRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.doneCh:
			return nil
		case <-ticker.C:
		}
		select {
		case <-s.doneCh:
			return nil
		default:
		}
		// Replaying existing routes recovers events missed during the outage.
		// Their detection latency includes the outage and retry delay.
		connection, err := subscribeRARouteConnection(true)
		if err != nil {
			log.Warnf("Failed to resubscribe to netlink routes: %v; retrying", err)
			continue
		}
		log.Info("Netlink route subscription restored; replaying existing routes (recovered latencies include the outage)")
		return connection
	}
}
