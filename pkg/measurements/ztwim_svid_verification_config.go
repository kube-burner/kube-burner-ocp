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

import "time"

const (
	ZtwimPerfWorkloadNamespace = "ztwim-perf"
	ZtwimPerfAppLabel          = "ztwim-perf"
	ZtwimPerfAppContainer      = "app"
)

// ZtwimSvidMeasurementConfig is set by the ztwim-svid-issuance workload before each run.
var ZtwimSvidMeasurementConfig struct {
	Namespace               string
	AppContainer            string
	AppLabel                string
	SVIDTTL                 time.Duration
	RotationSoak            time.Duration
	SvidLatencyMaxWait      time.Duration
	SvidLatencyPollInterval time.Duration
}

func ztwimSvidPollSettings(maxWait, pollInterval time.Duration) (time.Duration, time.Duration) {
	cfg := ZtwimSvidMeasurementConfig
	if maxWait <= 0 {
		if cfg.SvidLatencyMaxWait > 0 {
			maxWait = cfg.SvidLatencyMaxWait
		} else {
			maxWait = 5 * time.Minute
		}
	}
	if pollInterval <= 0 {
		if cfg.SvidLatencyPollInterval > 0 {
			pollInterval = cfg.SvidLatencyPollInterval
		} else {
			pollInterval = 2 * time.Second
		}
	}
	return maxWait, pollInterval
}

func ztwimSvidWorkloadTargets(namespace, appContainer, appLabel *string) {
	cfg := ZtwimSvidMeasurementConfig
	if *namespace == "" {
		if cfg.Namespace != "" {
			*namespace = cfg.Namespace
		} else {
			*namespace = ZtwimPerfWorkloadNamespace
		}
	}
	if *appContainer == "" {
		if cfg.AppContainer != "" {
			*appContainer = cfg.AppContainer
		} else {
			*appContainer = ZtwimPerfAppContainer
		}
	}
	if *appLabel == "" {
		if cfg.AppLabel != "" {
			*appLabel = cfg.AppLabel
		} else {
			*appLabel = ZtwimPerfAppLabel
		}
	}
}
