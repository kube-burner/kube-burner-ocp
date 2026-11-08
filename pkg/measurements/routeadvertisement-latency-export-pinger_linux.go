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
	firstReply, err := pingPod(ctx, target.pod, "")
	if !firstReply.IsZero() {
		results <- raExportPingResult{target: target, firstReply: firstReply}
	}
	if err != nil && !errors.Is(err, ctx.Err()) {
		results <- exportPingFailure(target, err)
	}
}

func exportPingFailure(target raExportPingTarget, err error) raExportPingResult {
	return raExportPingResult{
		target: target,
		err:    fmt.Errorf("verify export route %s for CUDN %s: %w", target.subnet, target.cudn, err),
	}
}
