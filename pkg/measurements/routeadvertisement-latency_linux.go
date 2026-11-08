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
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements/types"
	"github.com/kube-burner/kube-burner/v2/pkg/util/fileutils"
	raclient "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/routeadvertisements/v1/apis/clientset/versioned"
	udnclient "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/userdefinednetwork/v1/apis/clientset/versioned"
	log "github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var supportedRaLatencyJobTypes = []config.JobType{config.CreationJob, config.PatchJob}

// raLatency coordinates RA observation and the enabled connectivity scenarios.
type raLatency struct {
	measurements.BaseMeasurement

	// Shared targets discovered before observer and scenario startup.
	cudnSubnet map[string]cudnPods

	// Controls connectivity checks: none, export, import, or both.
	raConnectionCheck string

	observer       *raObserver
	importScenario *raImportScenario
	exportScenario *raExportScenario

	// Zero uses the normal ten-second completion polling interval.
	scenarioPollInterval time.Duration
}

func (r *raLatency) isExportEnabled() bool {
	return strings.Contains(r.raConnectionCheck, "export")
}

func (r *raLatency) isImportEnabled() bool {
	return strings.Contains(r.raConnectionCheck, "import")
}

type raLatencyMeasurementFactory struct {
	measurements.BaseMeasurementFactory
}

func NewRaLatencyMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]any, labelSelector string) (measurements.MeasurementFactory, error) {
	return raLatencyMeasurementFactory{
		measurements.NewBaseMeasurementFactory(configSpec, measurement, metadata, labelSelector),
	}, nil
}

func (plmf raLatencyMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config, embedCfg *fileutils.EmbedConfiguration) measurements.Measurement {
	return &raLatency{
		BaseMeasurement: plmf.NewBaseLatency(jobConfig, clientSet, restConfig, raLatencyMeasurement, raLatencyQuantilesMeasurement, embedCfg),
	}
}

// setInputVars applies route counts, scenario timeouts, and enabled checks from job objects.
func (r *raLatency) setInputVars() {
	var err error
	for _, obj := range r.JobConfig.Objects {
		if val, ok := obj.InputVars["numDummyIfaces"]; ok {
			numDummyIfaces = val.(int)
		}
		if val, ok := obj.InputVars["numAddressOnDummyIface"]; ok {
			numAddressOnDummyIface = val.(int)
		}
		if val, ok := obj.InputVars["exportScenarioMaxTimeout"]; ok {
			exportScenarioMaxTimeout, err = time.ParseDuration(val.(string))
			if err != nil {
				log.Errorf("Failure parsing exportScenarioMaxTimeout: %v", err)
			}
		}
		if val, ok := obj.InputVars["importScenarioMaxTimeout"]; ok {
			importScenarioMaxTimeout, err = time.ParseDuration(val.(string))
			if err != nil {
				log.Errorf("Failure parsing importScenarioMaxTimeout: %v", err)
			}
		}
		if val, ok := obj.InputVars["raConnectionCheck"]; ok {
			r.raConnectionCheck = val.(string)
		}
	}
}

// Start discovers targets and starts RA observation, followed by export workers
// when enabled. Import runs later in Stop. measurementWg signals initialization.
func (r *raLatency) Start(measurementWg *sync.WaitGroup) error {
	// Reset measurement and scenario state for multi-job benchmarks.
	r.LatencyQuantiles, r.NormLatencies = nil, nil
	r.Metrics = sync.Map{}
	r.observer = &raObserver{metrics: &r.Metrics}
	r.importScenario = &raImportScenario{metrics: &r.Metrics}
	r.exportScenario = &raExportScenario{}

	defer measurementWg.Done()
	if r.JobConfig.SkipIndexing {
		return nil
	}
	r.setInputVars()
	metricContext := raMetricContext{
		uuid:     r.Uuid,
		jobName:  r.JobConfig.Name,
		metadata: r.Metadata,
	}
	r.observer.metricContext = metricContext
	r.importScenario.metricContext = metricContext
	// Preserve partial discovery results on API errors, which discovery logs.
	r.cudnSubnet, _ = discoverCUDNPods(context.TODO(), r.ClientSet.CoreV1(), r.Uuid)

	// Acceptance timing is observed regardless of the connectivity mode.
	log.Infof("Creating Router Advertisement latency watcher for %s", r.JobConfig.Name)
	raClient, err := raclient.NewForConfig(r.RestConfig)
	if err != nil {
		log.Error(err)
		return err
	}
	udnClient, err := udnclient.NewForConfig(r.RestConfig)
	if err != nil {
		log.Error(err)
		return err
	}
	r.observer.start(raClient, udnClient.K8sV1().ClusterUserDefinedNetworks())
	if r.isExportEnabled() {
		if err := r.exportScenario.start(r.cudnSubnet); err != nil {
			r.observer.stop()
			return err
		}
	}
	return nil
}

// Collect only signals completion; informer callbacks and workers gather the data.
func (r *raLatency) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

// waitForScenarioCompletion polls the export, import, or acceptance counter every
// ten seconds until it reaches desiredCount. It returns an error on worker failure
// or timeout. The caller must still signal and wait for workers; this function
// neither stops them nor interrupts in-flight ping retries.
func (r *raLatency) waitForScenarioCompletion(desiredCount uint64, maxTimeout time.Duration, scenario string) error {
	var count uint64
	pollInterval := r.scenarioPollInterval
	if pollInterval <= 0 {
		pollInterval = 10 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	timeoutTimer := time.NewTimer(maxTimeout)
	defer timeoutTimer.Stop()

	for {
		select {
		case err := <-r.exportScenario.errors():
			return fmt.Errorf("%s scenario failed: %w", scenario, err)
		case err := <-r.importScenario.errors():
			return fmt.Errorf("%s scenario failed: %w", scenario, err)
		case <-ticker.C:
			switch scenario {
			case "export":
				count = r.exportScenario.verifiedCount()
			case "import":
				count = r.importScenario.verifiedCount()
			case "accepted":
				count = r.observer.acceptedCount()
			}
			log.Debugf("count %v , desiredCount %v", count, desiredCount)
			if count >= desiredCount {
				return nil
			}
		case <-timeoutTimer.C:
			log.Debugf("Timeout reached, signaling stop.")
			return fmt.Errorf("%s scenario timed out after %s: verified %d of %d routes", scenario, maxTimeout, count, desiredCount)
		}
	}
}

// Stop completes export before starting import, then normalizes and indexes
// metrics. Each scenario waits for its workers before returning an error.
func (r *raLatency) Stop() error {
	if r.JobConfig.SkipIndexing {
		return nil
	}
	defer r.observer.stop()

	if r.isExportEnabled() {
		log.Debugf("Export scenario is enabled")
		// Workers verify per CUDN; the target assumes one subnet per CUDN.
		err := r.waitForScenarioCompletion(uint64(len(r.cudnSubnet)), exportScenarioMaxTimeout, "export")
		stopErr := r.exportScenario.stop()
		if err != nil {
			return err
		}
		if stopErr != nil {
			return fmt.Errorf("export scenario failed: %w", stopErr)
		}
	} else {
		log.Debugf("Export scenario is not enabled")
	}

	if r.isImportEnabled() {
		log.Debugf("Import scenario is enabled")
		// Import starts only after export workers have exited.
		r.importScenario.numDummyIfaces = numDummyIfaces
		r.importScenario.numAddressOnDummyIface = numAddressOnDummyIface
		r.importScenario.expectedRouteCount = importRoutesCount
		if err := r.importScenario.start(r.cudnSubnet); err != nil {
			return err
		}
		importRoutesCount = int(r.importScenario.expectedCount())
		desiredCount := r.importScenario.expectedCount()
		err := r.waitForScenarioCompletion(desiredCount, importScenarioMaxTimeout, "import")
		r.importScenario.stop()
		r.importScenario.cleanup()
		if err != nil {
			return err
		}
	} else {
		log.Debugf("Import scenario is not enabled")
	}

	// With no ping checks, wait explicitly for acceptance before indexing.
	if !r.isExportEnabled() && !r.isImportEnabled() {
		log.Debugf("Waiting for RA Accepted conditions (ra-connection-check=none)")
		if err := r.waitForScenarioCompletion(uint64(r.JobConfig.JobIterations), exportScenarioMaxTimeout, "accepted"); err != nil {
			return err
		}
	}

	// Freeze observations before normalizing metrics.
	r.observer.stop()
	return r.StopMeasurement(r.normalizeMetrics, r.getLatency)
}

func (r *raLatency) normalizeMetrics() float64 {
	for _, m := range generateResults(&r.Metrics, r.exportScenario.results()) {
		switch m.Scenario {
		case "ExportRoutes":
			log.Debugf("%s: acceptedLatency=%v ms latency=%v ms netlinkRouteLatency=%v ms", m.Name, m.AcceptedLatency, m.Latency, m.NetlinkRouteLatency)
		case "ImportRoutes":
			log.Debugf("%s: importLatency=%v ms", m.Name, m.Latency)
		}
		r.NormLatencies = append(r.NormLatencies, m)
	}
	return 0
}

func (r *raLatency) IsCompatible() bool {
	return slices.Contains(supportedRaLatencyJobTypes, r.JobConfig.JobType)
}
