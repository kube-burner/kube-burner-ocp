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
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

const (
	exportPingInterval        = 100 * time.Millisecond
	exportPingEventsPerTarget = 2
)

type raExportPingTarget struct {
	cudn           string
	subnet         string
	pod            string
	routeTimestamp time.Time
}

type raExportPingResult struct {
	target     raExportPingTarget
	firstReply time.Time
	err        error
}

// runExportPinger emits one first reply and, at most, one terminal error. The
// owning worker supplies enough result capacity to join pingers before draining.
func runExportPinger(ctx context.Context, target raExportPingTarget, results chan<- raExportPingResult) {
	var firstReply sync.Once
	for ctx.Err() == nil {
		pinger, err := probing.NewPinger(target.pod)
		if err != nil {
			results <- exportPingFailure(target, err)
			return
		}
		pinger.Count = -1
		pinger.Interval = exportPingInterval
		pinger.RecordRtts = false
		pinger.RecordTTLs = false
		pinger.SetLogger(probing.NoopLogger{})
		pinger.OnRecv = func(_ *probing.Packet) {
			firstReply.Do(func() {
				results <- raExportPingResult{target: target, firstReply: time.Now().UTC()}
				pinger.Stop()
			})
		}
		var sendErr error
		pinger.OnSendError = func(_ *probing.Packet, err error) {
			log.Debugf("Export ping to pod %s failed: %v", target.pod, err)
			if !retryableExportPingError(err) {
				// pro-bing otherwise logs errors from later sends and keeps running.
				sendErr = err
				pinger.Stop()
			}
		}
		// RunWithContext also joins the library's sender and receiver goroutines.
		// The first reply stops the pinger; worker cancellation stops unanswered pings.
		err = pinger.RunWithContext(ctx)
		if sendErr != nil {
			results <- exportPingFailure(target, sendErr)
			return
		}
		if pinger.Statistics().PacketsRecv > 0 {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if !retryableExportPingError(err) {
			if err == nil {
				err = fmt.Errorf("pinger stopped before worker shutdown")
			}
			results <- exportPingFailure(target, err)
			return
		}
		// A transient error can terminate a pro-bing instance. Keep the logical
		// ping task alive by replacing that instance within the same worker.
		log.Debugf("Retrying export pinger for pod %s after network error: %v", target.pod, err)
		timer := time.NewTimer(exportPingInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func exportPingFailure(target raExportPingTarget, err error) raExportPingResult {
	return raExportPingResult{
		target: target,
		err:    fmt.Errorf("verify export route %s for CUDN %s: %w", target.subnet, target.cudn, err),
	}
}

func retryableExportPingError(err error) bool {
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return true
	}
	return errors.Is(err, unix.ENETUNREACH) || errors.Is(err, unix.EHOSTUNREACH) ||
		errors.Is(err, unix.ENETDOWN) || errors.Is(err, unix.EHOSTDOWN) ||
		errors.Is(err, unix.ECONNREFUSED) || errors.Is(err, unix.ENOBUFS) ||
		errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.ETIMEDOUT)
}
