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
	"errors"
	"net"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

const (
	raPingInterval = 100 * time.Millisecond
	// Scenario adapters emit at most a first reply and a terminal error per target.
	raPingEventsPerTarget = 2
)

// pingPod sends every 100 ms until the first reply, cancellation, or a fatal error.
// A source address selects the privileged socket required for import checks.
// It joins the sender and receiver before returning the first reply timestamp.
func pingPod(ctx context.Context, targetPod, sourceIP string) (time.Time, error) {
	var firstReply time.Time
	for ctx.Err() == nil {
		pinger, err := probing.NewPinger(targetPod)
		if err != nil {
			return firstReply, err
		}
		if sourceIP != "" {
			pinger.SetPrivileged(true)
			pinger.Source = sourceIP
		}
		pinger.Count = -1
		pinger.Interval = raPingInterval
		pinger.RecordRtts = false
		pinger.RecordTTLs = false
		pinger.SetLogger(probing.NoopLogger{})
		pinger.OnRecv = func(_ *probing.Packet) {
			if firstReply.IsZero() {
				firstReply = time.Now().UTC()
			}
			pinger.Stop()
		}
		var sendErr error
		pinger.OnSendError = func(_ *probing.Packet, err error) {
			log.Debugf("Ping to pod %s failed: %v", targetPod, err)
			if !retryablePingError(err) {
				// pro-bing otherwise logs errors from later sends and keeps running.
				sendErr = err
				pinger.Stop()
			}
		}
		// RunWithContext joins both goroutines before we read callback state.
		err = pinger.RunWithContext(ctx)
		if sendErr != nil {
			return firstReply, sendErr
		}
		if !firstReply.IsZero() {
			return firstReply, nil
		}
		if ctx.Err() != nil {
			return firstReply, ctx.Err()
		}
		if !retryablePingError(err) {
			if err == nil {
				err = errors.New("pinger stopped before receiving a reply or worker shutdown")
			}
			return firstReply, err
		}
		// A transient error can terminate a pro-bing instance. Replace it within
		// the same logical ping task and retain the worker's cancellation context.
		log.Debugf("Retrying pinger for pod %s after network error: %v", targetPod, err)
		timer := time.NewTimer(raPingInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return firstReply, ctx.Err()
		case <-timer.C:
		}
	}
	return firstReply, ctx.Err()
}

func retryablePingError(err error) bool {
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return true
	}
	return errors.Is(err, unix.ENETUNREACH) || errors.Is(err, unix.EHOSTUNREACH) ||
		errors.Is(err, unix.ENETDOWN) || errors.Is(err, unix.EHOSTDOWN) ||
		errors.Is(err, unix.ECONNREFUSED) || errors.Is(err, unix.ENOBUFS) ||
		errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.ETIMEDOUT)
}
