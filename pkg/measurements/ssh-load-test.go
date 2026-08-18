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
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements/types"
	"github.com/kube-burner/kube-burner/v2/pkg/util/fileutils"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	sshLoadTestMeasurementName          = "sshLoadTestMeasurement"
	sshLoadTestQuantilesMeasurementName = "sshLoadTestQuantilesMeasurement"
)

var supportedSSHLoadTestJobTypes = []config.JobType{config.CreationJob}

// SSHLoadTestResultsFilePath returns the path the beforeCleanup hook script
// (cudn-ssh-load-test.sh) writes its results to.
func SSHLoadTestResultsFilePath(uuid string) string {
	return fmt.Sprintf("/tmp/kube-burner-ssh-load-test-%s.json", uuid)
}

type sshLoadTestResult struct {
	TotalAttempts int `json:"totalAttempts"`
	Successes     int `json:"successes"`
	Failures      int `json:"failures"`
	DurationSecs  int `json:"durationSecs"`
	PodsTargeted  int `json:"podsTargeted"`
}

type sshLoadTestMetric struct {
	Timestamp     time.Time `json:"timestamp"`
	MetricName    string    `json:"metricName"`
	UUID          string    `json:"uuid"`
	JobName       string    `json:"jobName,omitempty"`
	Metadata      any       `json:"metadata,omitempty"`
	TotalAttempts int       `json:"totalAttempts"`
	Successes     int       `json:"successes"`
	Failures      int       `json:"failures"`
	DurationSecs  int       `json:"durationSecs"`
	PodsTargeted  int       `json:"podsTargeted"`
	// ConnPerSec is derived: TotalAttempts / DurationSecs
	ConnPerSec float64 `json:"connectionsPerSecond"`
	// FailureRate as percentage
	FailureRate float64 `json:"failureRate"`
}

type sshLoadTest struct {
	measurements.BaseMeasurement
}

type sshLoadTestMeasurementFactory struct {
	measurements.BaseMeasurementFactory
}

func NewSSHLoadTestMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]any, labelSelector string) (measurements.MeasurementFactory, error) {
	return sshLoadTestMeasurementFactory{
		measurements.NewBaseMeasurementFactory(configSpec, measurement, metadata, labelSelector),
	}, nil
}

func (f sshLoadTestMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config, embedCfg *fileutils.EmbedConfiguration) measurements.Measurement {
	return &sshLoadTest{
		BaseMeasurement: f.NewBaseLatency(jobConfig, clientSet, restConfig, sshLoadTestMeasurementName, sshLoadTestQuantilesMeasurementName, embedCfg),
	}
}

func (s *sshLoadTest) Start(measurementWg *sync.WaitGroup) error {
	defer measurementWg.Done()
	s.LatencyQuantiles, s.NormLatencies = nil, nil
	s.Metrics = sync.Map{}
	return nil
}

func (s *sshLoadTest) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

func (s *sshLoadTest) Stop() error {
	if s.JobConfig.SkipIndexing {
		return nil
	}
	if err := s.loadResults(); err != nil {
		log.Errorf("sshLoadTest: failed to load results for job %s: %v", s.JobConfig.Name, err)
	}
	return s.StopMeasurement(s.normalizeMetrics, s.getLatency)
}

func (s *sshLoadTest) loadResults() error {
	path := SSHLoadTestResultsFilePath(s.Uuid)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading ssh load test results file %s: %w", path, err)
	}

	var result sshLoadTestResult
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parsing ssh load test results from %s: %w", path, err)
	}

	var connPerSec float64
	if result.DurationSecs > 0 {
		connPerSec = float64(result.TotalAttempts) / float64(result.DurationSecs)
	}
	var failureRate float64
	if result.TotalAttempts > 0 {
		failureRate = float64(result.Failures) / float64(result.TotalAttempts) * 100.0
	}

	metric := sshLoadTestMetric{
		Timestamp:     time.Now().UTC(),
		MetricName:    sshLoadTestMeasurementName,
		UUID:          s.Uuid,
		JobName:       s.JobConfig.Name,
		Metadata:      s.Metadata,
		TotalAttempts: result.TotalAttempts,
		Successes:     result.Successes,
		Failures:      result.Failures,
		DurationSecs:  result.DurationSecs,
		PodsTargeted:  result.PodsTargeted,
		ConnPerSec:    connPerSec,
		FailureRate:   failureRate,
	}

	log.Infof("sshLoadTest: %d total attempts (%d successes, %d failures) over %ds targeting %d pods — %.1f conn/s, %.2f%% failure rate",
		result.TotalAttempts, result.Successes, result.Failures, result.DurationSecs, result.PodsTargeted, connPerSec, failureRate)

	s.Metrics.Store("load-test-summary", metric)
	return nil
}

func (s *sshLoadTest) normalizeMetrics() float64 {
	var failRate float64
	s.Metrics.Range(func(key, value any) bool {
		m := value.(sshLoadTestMetric)
		failRate = m.FailureRate
		s.NormLatencies = append(s.NormLatencies, m)
		return true
	})
	return failRate
}

func (s *sshLoadTest) getLatency(normLatency any) map[string]float64 {
	m := normLatency.(sshLoadTestMetric)
	return map[string]float64{
		"connectionsPerSecond": m.ConnPerSec,
	}
}

func (s *sshLoadTest) IsCompatible() bool {
	return slices.Contains(supportedSSHLoadTestJobTypes, s.JobConfig.JobType)
}
