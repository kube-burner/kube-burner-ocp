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
	"time"
)

type raImportPingTarget struct {
	route          routeImport
	sourceIP       string
	routeTimestamp time.Time
}

type raImportPingResult struct {
	target  raImportPingTarget
	latency float64
	err     error
}

// runImportPinger emits one first reply and, at most, one terminal error. The
// owning worker supplies enough result capacity to join pingers before draining.
func runImportPinger(ctx context.Context, target raImportPingTarget, results chan<- raImportPingResult) {
	startedAt := time.Now().UTC()
	firstReply, err := pingPod(ctx, target.route.pod, target.sourceIP)
	if !firstReply.IsZero() {
		results <- raImportPingResult{
			target:  target,
			latency: float64(firstReply.Sub(startedAt).Milliseconds()),
		}
	}
	if err != nil && !errors.Is(err, ctx.Err()) {
		results <- raImportPingResult{
			target: target,
			err:    fmt.Errorf("verify import route %s: %w", target.route.addr, err),
		}
	}
}
