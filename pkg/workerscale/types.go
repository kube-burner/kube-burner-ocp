// Copyright 2024 The Kube-burner Authors.
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

package workerscale

import (
	"time"

	"github.com/cloud-bulldozer/go-commons/indexers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Interface for our scenarios
type Scenario interface {
	OrchestrateWorkload(ScaleConfig)
}

// ScaleConfig contains configuration for scaling
type ScaleConfig struct {
	UUID                  string
	AdditionalWorkerNodes int
	Metadata              map[string]interface{}
	Indexer               indexers.Indexer
	GC                    bool
	ScaleEventEpoch       int64
	AutoScalerEnabled     bool
}

// Struct to extract AMIID from aws provider spec
type AWSProviderSpec struct {
	AMI struct {
		ID string `json:"id"`
	} `json:"ami"`
}

// MachineInfo provides information about a machine resource
type MachineInfo struct {
	nodeUID           string
	creationTimestamp time.Time
	readyTimestamp    time.Time
}

// MachineSetInfo provides information about a machineset resource
type MachineSetInfo struct {
	lastUpdatedTime time.Time
	prevReplicas    int
	currentReplicas int
}

// ProviderStatusCondition of a machine
type ProviderStatusCondition struct {
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`
	Message            string      `json:"message"`
	Reason             string      `json:"reason"`
	Status             string      `json:"status"`
	Type               string      `json:"type"`
}

// ProviderStatus of a machine
type ProviderStatus struct {
	Conditions []ProviderStatusCondition `json:"conditions"`
}

// NodeReadyMetric to capture details on node bootup
type NodeReadyMetric struct {
	ScaleEventTimestamp      time.Time         `json:"-"`
	MachineCreationTimestamp time.Time         `json:"-"`
	MachineCreationLatency   int               `json:"machineCreationLatency"`
	MachineReadyTimestamp    time.Time         `json:"-"`
	MachineReadyLatency      int               `json:"machineReadyLatency"`
	NodeCreationTimestamp    time.Time         `json:"-"`
	NodeCreationLatency      int               `json:"nodeCreationLatency"`
	NodeReadyTimestamp       time.Time         `json:"-"`
	NodeReadyLatency         int               `json:"nodeReadyLatency"`
	MetricName               string            `json:"metricName"`
	AMIID                    string            `json:"amiID"`
	UUID                     string            `json:"uuid"`
	JobName                  string            `json:"jobName,omitempty"`
	Name                     string            `json:"nodeName"`
	Labels                   map[string]string `json:"labels"`
}
