// Copyright 2023 The go-commons Authors.
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

package prometheus

import (
	"net/http"

	apiv1 "github.com/prometheus/client_golang/api/prometheus/v1"
)

type Aggregation string

const (
	Avg   Aggregation = "avg"
	Max   Aggregation = "max"
	Min   Aggregation = "min"
	P99   Aggregation = "99"
	P95   Aggregation = "95"
	P90   Aggregation = "90"
	P50   Aggregation = "50"
	Stdev Aggregation = "stdev"
)

// Prometheus describes the prometheus connection
type Prometheus struct {
	api      apiv1.API
	Endpoint string
}

// This object implements RoundTripper
type authTransport struct {
	Transport http.RoundTripper
	token     string
	username  string
	password  string
}
