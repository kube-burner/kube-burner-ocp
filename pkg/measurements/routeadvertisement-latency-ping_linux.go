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
	"time"

	probing "github.com/prometheus-community/pro-bing"
	log "github.com/sirupsen/logrus"
)

const pingAttempts = 100

// pingAddress sends one ping, optionally bound to a source address.
func pingAddress(srcIP, destIP string, pingerTimeoutMsec int) error {
	pinger, err := probing.NewPinger(destIP)
	if err != nil {
		log.Debugf("Failed to create pinger for %s: %v", destIP, err)
		return err
	}

	if srcIP != "" {
		// Required for raw sockets (root access needed)
		pinger.SetPrivileged(true)
		// Bind to the specified source IP
		pinger.Source = srcIP
	}
	pinger.Count = 1
	pinger.Timeout = time.Duration(pingerTimeoutMsec) * time.Millisecond
	if err := pinger.Run(); err != nil {
		log.Debugf("Ping to %s failed: %v", destIP, err)
		return err
	}
	if pinger.Statistics().PacketsRecv == 0 {
		return fmt.Errorf("ping to %s timed out without receiving a reply", destIP)
	}
	return nil
}
