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
	"bufio"
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
	sshCheckMeasurementName          = "sshCheckMeasurement"
	sshCheckQuantilesMeasurementName = "sshCheckQuantilesMeasurement"
	// sshCheckMissingResultsSentinelMs marks failed checks, which are excluded
	// from latency quantiles but still counted towards the failure rate that
	// BaseMeasurement.StopMeasurement compares against its 10% threshold.
	sshCheckMissingResultsSentinelMs = -1
)

var supportedSSHCheckJobTypes = []config.JobType{config.CreationJob}

// SSHCheckResultsFilePath returns the path the beforeCleanup hook script
// (cudn-ssh-test.sh) writes its results to, and the sshCheck measurement
// reads them back from. Both sides derive this from the run's UUID, which is
// already passed to the hook as {{.UUID}}.
func SSHCheckResultsFilePath(uuid string) string {
	return fmt.Sprintf("/tmp/kube-burner-ssh-results-%s.ndjson", uuid)
}

// sshCheckResult is one NDJSON line written by cudn-ssh-test.sh, one per
// server pod it attempted to reach.
type sshCheckResult struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	IP        string `json:"ip"`
	Success   bool   `json:"success"`
	LatencyMs int    `json:"latencyMs"`
}

type sshCheckMetric struct {
	Timestamp  time.Time `json:"timestamp"`
	MetricName string    `json:"metricName"`
	UUID       string    `json:"uuid"`
	JobName    string    `json:"jobName,omitempty"`
	Namespace  string    `json:"namespace"`
	Pod        string    `json:"podName"`
	IP         string    `json:"podIP"`
	Metadata   any       `json:"metadata,omitempty"`
	Success    bool      `json:"success"`
	// SSHLatency is the connection latency in milliseconds for successful
	// checks, or sshCheckMissingResultsSentinelMs for failed ones.
	SSHLatency int `json:"sshLatency"`
}

type sshCheck struct {
	measurements.BaseMeasurement
}

type sshCheckMeasurementFactory struct {
	measurements.BaseMeasurementFactory
}

// NewSSHCheckMeasurementFactory builds the sshCheck measurement. It doesn't
// watch the Kubernetes API at all: the actual SSH connectivity check runs as
// a beforeCleanup hook script (on the bastion host, with a route to the CUDN
// pod network), which writes its results to a UUID-keyed file. Since
// HookBeforeCleanup fires before measurementsInstance.Stop()/.Index() in
// kube-burner's job loop, this measurement's Stop() reads that file and
// routes the results through the normal indexing pipeline.
func NewSSHCheckMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]any, labelSelector string) (measurements.MeasurementFactory, error) {
	return sshCheckMeasurementFactory{
		measurements.NewBaseMeasurementFactory(configSpec, measurement, metadata, labelSelector),
	}, nil
}

func (scmf sshCheckMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config, embedCfg *fileutils.EmbedConfiguration) measurements.Measurement {
	return &sshCheck{
		BaseMeasurement: scmf.NewBaseLatency(jobConfig, clientSet, restConfig, sshCheckMeasurementName, sshCheckQuantilesMeasurementName, embedCfg),
	}
}

// Start is a no-op: there is nothing to watch via informer, all the work
// happens in the beforeCleanup hook script and is picked up in Stop().
func (s *sshCheck) Start(measurementWg *sync.WaitGroup) error {
	defer measurementWg.Done()
	s.LatencyQuantiles, s.NormLatencies = nil, nil
	s.Metrics = sync.Map{}
	return nil
}

func (s *sshCheck) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

func (s *sshCheck) Stop() error {
	if s.JobConfig.SkipIndexing {
		return nil
	}
	if err := s.loadResults(); err != nil {
		log.Errorf("sshCheck: failed to load results for job %s: %v", s.JobConfig.Name, err)
	}
	return s.StopMeasurement(s.normalizeMetrics, s.getLatency)
}

func (s *sshCheck) loadResults() error {
	path := SSHCheckResultsFilePath(s.Uuid)
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening ssh check results file %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if line == "" {
			continue
		}
		var result sshCheckResult
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			log.Warnf("sshCheck: skipping malformed results line %d in %s: %v", lineNum, path, err)
			continue
		}
		latency := result.LatencyMs
		if !result.Success {
			latency = sshCheckMissingResultsSentinelMs
		}
		key := fmt.Sprintf("%s/%s", result.Namespace, result.Pod)
		s.Metrics.Store(key, sshCheckMetric{
			Timestamp:  time.Now().UTC(),
			MetricName: sshCheckMeasurementName,
			UUID:       s.Uuid,
			JobName:    s.JobConfig.Name,
			Namespace:  result.Namespace,
			Pod:        result.Pod,
			IP:         result.IP,
			Metadata:   s.Metadata,
			Success:    result.Success,
			SSHLatency: latency,
		})
	}
	return scanner.Err()
}

func (s *sshCheck) normalizeMetrics() float64 {
	total := 0
	failed := 0
	s.Metrics.Range(func(key, value any) bool {
		m := value.(sshCheckMetric)
		total++
		if !m.Success {
			failed++
		}
		s.NormLatencies = append(s.NormLatencies, m)
		return true
	})
	if total == 0 {
		log.Warn("sshCheck: no results found, was the beforeCleanup hook wired in and did it run before this measurement stopped?")
		return 0.0
	}
	failRate := float64(failed) / float64(total) * 100.0
	log.Infof("sshCheck: %d/%d SSH checks succeeded (%.2f%% failure rate)", total-failed, total, failRate)
	return failRate
}

func (s *sshCheck) getLatency(normLatency any) map[string]float64 {
	m := normLatency.(sshCheckMetric)
	if !m.Success {
		return map[string]float64{}
	}
	return map[string]float64{
		"SSHLatency": float64(m.SSHLatency),
	}
}

func (s *sshCheck) IsCompatible() bool {
	return slices.Contains(supportedSSHCheckJobTypes, s.JobConfig.JobType)
}
