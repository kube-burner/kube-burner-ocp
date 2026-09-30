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
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/measurements/types"
	"github.com/kube-burner/kube-burner/v2/pkg/util"
	"github.com/kube-burner/kube-burner/v2/pkg/util/fileutils"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

const (
	agenticRunLatencyMeasurement          = "agenticRunLatencyMeasurement"
	agenticRunLatencyQuantilesMeasurement = "agenticRunLatencyQuantilesMeasurement"

	agenticRunKind       = "AgenticRun"
	agenticRunAPIVersion = "agentic.openshift.io/v1alpha1"

	// AgenticRun status condition types. Phase is never stored on the CR, it is
	// derived from these conditions (agentic-operator decision 0020).
	conditionAnalyzed         = "Analyzed"
	conditionApproved         = "Approved"
	conditionExecuted         = "Executed"
	conditionVerified         = "Verified"
	conditionEscalated        = "Escalated"
	conditionDenied           = "Denied"
	conditionEmergencyStopped = "EmergencyStopped"

	// reasonNoActionRequired on a True Analyzed condition short-circuits the
	// workflow: the run terminates without proposing remediation options.
	reasonNoActionRequired = "NoActionRequired"

	// Latency series names. The first group is cumulative from AgenticRun
	// creation, the second group is the duration of an individual phase.
	latencyAnalyzed          = "Analyzed"
	latencyApproved          = "Approved"
	latencyExecuted          = "Executed"
	latencyVerified          = "Verified"
	latencyEscalated         = "Escalated"
	latencyTerminal          = "Terminal"
	latencyAnalysisPhase     = "AnalysisPhase"
	latencyApprovalPhase     = "ApprovalPhase"
	latencyExecutionPhase    = "ExecutionPhase"
	latencyVerificationPhase = "VerificationPhase"
	latencyEscalationPhase   = "EscalationPhase"
	latencySandboxStartup    = "SandboxStartup"

	// Per-step sandbox startup series. A run provisions at most four sandbox
	// pods, one per agent step, and they run strictly serially. Approval has no
	// sandbox, it is a controller side gate that waits on an approval decision.
	latencySandboxStartupAnalysis     = "SandboxStartupAnalysis"
	latencySandboxStartupExecution    = "SandboxStartupExecution"
	latencySandboxStartupVerification = "SandboxStartupVerification"
	latencySandboxStartupEscalation   = "SandboxStartupEscalation"

	// Agent step names, as they appear in the sandbox pod name built by
	// SandboxManager.Create, ls-{step}-{agenticRunName} truncated to 63 chars.
	stepAnalysis     = "analysis"
	stepExecution    = "execution"
	stepVerification = "verification"
	stepEscalation   = "escalation"

	// sandboxPodNamePrefix is the fixed first segment of every sandbox pod name
	sandboxPodNamePrefix = "ls"

	// Derived phases
	phasePending          = "Pending"
	phaseProposed         = "Proposed"
	phaseExecuted         = "Executed"
	phaseCompleted        = "Completed"
	phaseEscalated        = "Escalated"
	phaseDenied           = "Denied"
	phaseFailed           = "Failed"
	phaseEmergencyStopped = "EmergencyStopped"
	phaseNoActionRequired = "NoActionRequired"
)

var (
	supportedAgenticRunConditions = map[string]struct{}{
		latencyAnalyzed:          {},
		latencyApproved:          {},
		latencyExecuted:          {},
		latencyVerified:          {},
		latencyEscalated:         {},
		latencyTerminal:          {},
		latencyAnalysisPhase:     {},
		latencyApprovalPhase:     {},
		latencyExecutionPhase:    {},
		latencyVerificationPhase: {},
		latencyEscalationPhase:   {},
		latencySandboxStartup:    {},

		latencySandboxStartupAnalysis:     {},
		latencySandboxStartupExecution:    {},
		latencySandboxStartupVerification: {},
		latencySandboxStartupEscalation:   {},
	}

	// sandboxSteps maps an agent step to its per-step startup series
	sandboxSteps = map[string]string{
		stepAnalysis:     latencySandboxStartupAnalysis,
		stepExecution:    latencySandboxStartupExecution,
		stepVerification: latencySandboxStartupVerification,
		stepEscalation:   latencySandboxStartupEscalation,
	}

	supportedAgenticRunLatencyJobTypes = map[config.JobType]struct{}{
		config.CreationJob: {},
		config.PatchJob:    {},
	}

	// trackedFailureConditions are the conditions whose False status marks the run as failed
	trackedFailureConditions = map[string]struct{}{
		conditionAnalyzed:  {},
		conditionApproved:  {},
		conditionExecuted:  {},
		conditionVerified:  {},
		conditionEscalated: {},
	}

	// agenticRunSandboxLabel is the pod label key holding the AgenticRun UID.
	// Only needed when sandbox pods carry no ownerReference to their run. It is
	// set from the agentic-run-density --sandbox-label flag rather than from the
	// measurement YAML, because types.Measurement lives upstream and must not
	// grow OpenShift Lightspeed specific fields.
	agenticRunSandboxLabel string
)

// SetAgenticRunSandboxLabel configures the fallback pod label used to correlate
// sandbox pods with their AgenticRun. Call before the workload starts.
func SetAgenticRunSandboxLabel(label string) {
	agenticRunSandboxLabel = label
}

// agenticRunObject is the minimal typed view of an AgenticRun CR. The agentic
// API has no published Go module, and only metadata plus status conditions are
// needed to time the workflow.
type agenticRunObject struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Status            struct {
		Conditions []metav1.Condition `json:"conditions,omitempty"`
	} `json:"status,omitempty"`
}

// sandboxTiming is the creation to Ready window of one sandbox pod
type sandboxTiming struct {
	created time.Time
	ready   time.Time
}

// merge folds a later observation of the same pod in, keeping the earliest
// creation and the first Ready transition seen
func (t *sandboxTiming) merge(other sandboxTiming) {
	if t.created.IsZero() || (!other.created.IsZero() && other.created.Before(t.created)) {
		t.created = other.created
	}
	if t.ready.IsZero() {
		t.ready = other.ready
	}
}

// agenticRunMetric holds the lifecycle of a single AgenticRun. An AgenticRun is
// reconciled asynchronously along Analysis -> Approval -> Execution ->
// Verification, with Escalation as a branch off a failed verification rather
// than a further stage, so a run reports either the verified path or the
// escalated one but never both. Analysis, Execution, Verification and
// Escalation each run in their own ephemeral sandbox pod; Approval does not,
// it is a controller side gate. Both cumulative and per-phase latencies are
// recorded.
type agenticRunMetric struct {
	// Timestamp is the AgenticRun creationTimestamp (t0)
	Timestamp time.Time `json:"timestamp"`

	// Condition transition times, unexported and only used to compute latencies
	analyzed         time.Time
	approved         time.Time
	executed         time.Time
	verified         time.Time
	escalated        time.Time
	denied           time.Time
	emergencyStopped time.Time
	failed           time.Time
	terminal         time.Time

	// Sandbox pod lifecycle. sandboxFirst is the earliest created sandbox of
	// the run, tracked by pod UID so repeated informer events for that pod fill
	// in its Ready transition instead of being mistaken for another pod.
	sandboxFirst        sandboxTiming
	sandboxFirstUID     string
	sandboxAnalysis     sandboxTiming
	sandboxExecution    sandboxTiming
	sandboxVerification sandboxTiming
	sandboxEscalation   sandboxTiming

	// Cumulative latencies from AgenticRun creation (ms)
	AnalyzedLatency  int `json:"analyzedLatency"`
	ApprovedLatency  int `json:"approvedLatency"`
	ExecutedLatency  int `json:"executedLatency"`
	VerifiedLatency  int `json:"verifiedLatency"`
	EscalatedLatency int `json:"escalatedLatency"`
	// TerminalLatency is the end to end latency, creation to terminal phase (ms)
	TerminalLatency int `json:"terminalLatency"`

	// Duration of each individual phase (ms). ApprovalPhaseLatency is the odd
	// one out: it is wall-clock time waiting on an approval decision, not
	// compute. Under a human reviewer it dwarfs every other series and makes
	// TerminalLatency meaningless; under an auto-approving ApprovalPolicy it
	// collapses to reconcile noise. Read it separately from the rest.
	AnalysisPhaseLatency     int `json:"analysisPhaseLatency"`
	ApprovalPhaseLatency     int `json:"approvalPhaseLatency"`
	ExecutionPhaseLatency    int `json:"executionPhaseLatency"`
	VerificationPhaseLatency int `json:"verificationPhaseLatency"`
	EscalationPhaseLatency   int `json:"escalationPhaseLatency"`

	// SandboxStartupLatency is creation to Ready of the earliest sandbox pod (ms)
	SandboxStartupLatency int `json:"sandboxStartupLatency"`
	// Per-step sandbox startup, creation to Ready of that step's pod (ms).
	// Zero when the run never reached the step, or when the pod name does not
	// follow the ls-{step}-{run} convention, as in sandbox-claim mode.
	SandboxStartupAnalysisLatency     int `json:"sandboxStartupAnalysisLatency"`
	SandboxStartupExecutionLatency    int `json:"sandboxStartupExecutionLatency"`
	SandboxStartupVerificationLatency int `json:"sandboxStartupVerificationLatency"`
	SandboxStartupEscalationLatency   int `json:"sandboxStartupEscalationLatency"`
	// SandboxPodCount is the number of sandbox pods the operator created for
	// this run, at most four and typically three on a clean verified run
	SandboxPodCount int `json:"sandboxPodCount"`

	// Phase is derived from the observed conditions, mirroring the operator's DerivePhase
	Phase string `json:"phase"`
	// AnalyzedReason is the reason of the Analyzed condition, NoActionRequired short-circuits the run
	AnalyzedReason string `json:"analyzedReason,omitempty"`
	// FailureReason is the reason of the first condition observed with status False
	FailureReason string `json:"failureReason,omitempty"`

	MetricName   string `json:"metricName"`
	UUID         string `json:"uuid"`
	JobName      string `json:"jobName,omitempty"`
	JobIteration int    `json:"jobIteration"`
	Replica      int    `json:"replica"`
	Namespace    string `json:"namespace"`
	Name         string `json:"agenticRunName"`
	ChurnMetric  bool   `json:"churnMetric,omitempty"`
	Metadata     any    `json:"metadata,omitempty"`
}

type agenticRunLatency struct {
	measurements.BaseMeasurement
	// jobNamespaces scopes the sandbox pod watcher, sandbox pods are created by
	// the operator and therefore carry no kube-burner labels
	jobNamespaces map[string]struct{}
	// sandboxPods maps sandbox pod UID -> AgenticRun UID, used to count pods per run
	sandboxPods   sync.Map
	dynamicClient dynamic.Interface
	stopCh        chan struct{}
}

type agenticRunLatencyMeasurementFactory struct {
	measurements.BaseMeasurementFactory
}

// NewAgenticRunLatencyMeasurementFactory builds the agenticRunLatency measurement.
// It lives in kube-burner-ocp rather than upstream because AgenticRun is an
// OpenShift Lightspeed CRD shipped only on OCP >= 5.0.
func NewAgenticRunLatencyMeasurementFactory(configSpec config.Spec, measurement types.Measurement, metadata map[string]any, labelSelector string) (measurements.MeasurementFactory, error) {
	if err := verifyAgenticRunThresholds(measurement); err != nil {
		return nil, err
	}
	return agenticRunLatencyMeasurementFactory{
		measurements.NewBaseMeasurementFactory(configSpec, measurement, metadata, labelSelector),
	}, nil
}

func (arf agenticRunLatencyMeasurementFactory) NewMeasurement(jobConfig *config.Job, clientSet kubernetes.Interface, restConfig *rest.Config, embedCfg *fileutils.EmbedConfiguration) measurements.Measurement {
	return &agenticRunLatency{
		BaseMeasurement: arf.NewBaseLatency(jobConfig, clientSet, restConfig, agenticRunLatencyMeasurement, agenticRunLatencyQuantilesMeasurement, embedCfg),
		dynamicClient:   dynamic.NewForConfigOrDie(restConfig),
	}
}

// verifyAgenticRunThresholds rejects thresholds referencing a series this
// measurement does not emit, which would otherwise silently never fire
func verifyAgenticRunThresholds(measurement types.Measurement) error {
	for _, threshold := range measurement.LatencyThresholds {
		if _, supported := supportedAgenticRunConditions[threshold.ConditionType]; !supported {
			return fmt.Errorf("unsupported agenticRunLatency threshold conditionType: %s", threshold.ConditionType)
		}
	}
	return nil
}

func (a *agenticRunLatency) handleCreateAgenticRun(obj any) {
	run, err := util.ConvertAnyToTyped[agenticRunObject](obj)
	if err != nil {
		log.Errorf("agenticRunLatency: failed to convert to AgenticRun: %v", err)
		return
	}
	runLabels := run.GetLabels()
	a.Metrics.LoadOrStore(string(run.UID), agenticRunMetric{
		Timestamp:    run.CreationTimestamp.UTC(),
		Namespace:    run.Namespace,
		Name:         run.Name,
		Phase:        phasePending,
		MetricName:   agenticRunLatencyMeasurement,
		UUID:         a.Uuid,
		JobName:      a.JobConfig.Name,
		Metadata:     a.Metadata,
		JobIteration: intFromLabels(runLabels, config.KubeBurnerLabelJobIteration),
		Replica:      intFromLabels(runLabels, config.KubeBurnerLabelReplica),
	})
	// The CR may already carry conditions when the informer performs its initial list
	a.recordConditions(run)
}

func (a *agenticRunLatency) handleUpdateAgenticRun(obj any) {
	run, err := util.ConvertAnyToTyped[agenticRunObject](obj)
	if err != nil {
		log.Errorf("agenticRunLatency: failed to convert to AgenticRun: %v", err)
		return
	}
	a.recordConditions(run)
}

func (a *agenticRunLatency) recordConditions(run *agenticRunObject) {
	value, exists := a.Metrics.Load(string(run.UID))
	if !exists {
		return
	}
	m := value.(agenticRunMetric)
	// Once the run is terminal its conditions no longer change
	if !m.terminal.IsZero() {
		return
	}
	for _, condition := range run.Status.Conditions {
		transitionTime := condition.LastTransitionTime.UTC()
		if transitionTime.IsZero() {
			transitionTime = time.Now().UTC()
		}
		switch condition.Status {
		case metav1.ConditionTrue:
			switch condition.Type {
			case conditionAnalyzed:
				if m.analyzed.IsZero() {
					m.analyzed, m.AnalyzedReason = transitionTime, condition.Reason
				}
			case conditionApproved:
				if m.approved.IsZero() {
					m.approved = transitionTime
				}
			case conditionExecuted:
				if m.executed.IsZero() {
					m.executed = transitionTime
				}
			case conditionVerified:
				if m.verified.IsZero() {
					m.verified = transitionTime
				}
			case conditionEscalated:
				if m.escalated.IsZero() {
					m.escalated = transitionTime
				}
			case conditionDenied:
				if m.denied.IsZero() {
					m.denied = transitionTime
				}
			case conditionEmergencyStopped:
				if m.emergencyStopped.IsZero() {
					m.emergencyStopped = transitionTime
				}
			}
		case metav1.ConditionFalse:
			// A phase condition flipped to False with a reason is a run failure,
			// this also covers the CancelledByUser cancellation path
			if _, tracked := trackedFailureConditions[condition.Type]; tracked && condition.Reason != "" && m.failed.IsZero() {
				m.failed, m.FailureReason = transitionTime, condition.Reason
			}
		}
	}
	m.Phase = derivePhase(&m)
	m.terminal = terminalTime(&m)
	a.Metrics.Store(string(run.UID), m)
}

// handleSandboxPod correlates the operator created sandbox pods back to their
// AgenticRun. Those pods are not created by kube-burner, so they are matched
// through their ownerReference, falling back to the configured UID label.
func (a *agenticRunLatency) handleSandboxPod(obj any) {
	pod, err := util.ConvertAnyToTyped[corev1.Pod](obj)
	if err != nil {
		log.Errorf("agenticRunLatency: failed to convert to Pod: %v", err)
		return
	}
	if _, ok := a.jobNamespaces[pod.Namespace]; !ok {
		return
	}
	runUID := sandboxRunUID(pod)
	if runUID == "" {
		return
	}
	value, ok := a.Metrics.Load(runUID)
	if !ok {
		return
	}
	a.sandboxPods.Store(string(pod.UID), runUID)
	m := value.(agenticRunMetric)

	timing := sandboxTiming{created: pod.CreationTimestamp.UTC()}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			timing.ready = c.LastTransitionTime.UTC()
			break
		}
	}

	// Run level startup tracks the earliest created sandbox, normally analysis.
	// Matching on pod UID keeps a later event for that same pod folding into it
	// rather than being compared as a different pod.
	switch {
	case m.sandboxFirstUID == "" || timing.created.Before(m.sandboxFirst.created):
		m.sandboxFirstUID, m.sandboxFirst = string(pod.UID), timing
	case m.sandboxFirstUID == string(pod.UID):
		m.sandboxFirst.merge(timing)
	}

	// Per-step startup, so a slow execution sandbox is distinguishable from a
	// slow analysis one instead of both collapsing into a single series
	switch sandboxStep(pod) {
	case stepAnalysis:
		m.sandboxAnalysis.merge(timing)
	case stepExecution:
		m.sandboxExecution.merge(timing)
	case stepVerification:
		m.sandboxVerification.merge(timing)
	case stepEscalation:
		m.sandboxEscalation.merge(timing)
	}
	a.Metrics.Store(runUID, m)
}

// sandboxStep recovers the agent step from the sandbox pod name, which
// SandboxManager.Create builds as ls-{step}-{agenticRunName}. Returns an empty
// string for anything that does not match, so sandbox-claim mode pods still
// count towards SandboxPodCount and the run level startup series without
// inventing a step for them.
func sandboxStep(pod *corev1.Pod) string {
	parts := strings.SplitN(pod.Name, "-", 3)
	if len(parts) < 3 || parts[0] != sandboxPodNamePrefix {
		return ""
	}
	if _, known := sandboxSteps[parts[1]]; !known {
		return ""
	}
	return parts[1]
}

func sandboxRunUID(pod *corev1.Pod) string {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == agenticRunKind {
			return string(owner.UID)
		}
	}
	if agenticRunSandboxLabel != "" {
		return pod.Labels[agenticRunSandboxLabel]
	}
	return ""
}

// Start begins the agenticRunLatency measurement. The informers are built here
// rather than through the upstream startMeasurement helper, which is not
// exported across the module boundary. Doing it locally also makes the
// selector asymmetry explicit: AgenticRuns are created by kube-burner and so
// carry its labels, while sandbox pods are created by the agentic-operator and
// must be watched unfiltered, then narrowed by namespace.
func (a *agenticRunLatency) Start(measurementWg *sync.WaitGroup) error {
	defer measurementWg.Done()

	a.LatencyQuantiles, a.NormLatencies = nil, nil
	a.Metrics = sync.Map{}
	a.sandboxPods = sync.Map{}

	if a.JobConfig.SkipIndexing {
		return nil
	}

	runGVR, err := util.ResourceToGVR(a.RestConfig, agenticRunKind, agenticRunAPIVersion)
	if err != nil {
		return fmt.Errorf("error getting GVR for %s: %w. Is the agentic layer installed on the cluster?", agenticRunKind, err)
	}
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	a.jobNamespaces = agenticJobNamespaces(a.JobConfig)
	a.stopCh = make(chan struct{})

	// AgenticRun watcher, scoped by the kube-burner label selector
	runFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(a.dynamicClient, 0, "",
		func(options *metav1.ListOptions) {
			options.LabelSelector = a.LabelSelector
		})
	runInformer := runFactory.ForResource(runGVR).Informer()
	if err := runInformer.SetTransform(agenticRunTransformFunc()); err != nil {
		log.Errorf("agenticRunLatency: failed to set AgenticRun transform: %v", err)
	}
	if _, err := runInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: a.handleCreateAgenticRun,
		UpdateFunc: func(oldObj, newObj any) {
			a.handleUpdateAgenticRun(newObj)
		},
	}); err != nil {
		return fmt.Errorf("error adding AgenticRun event handler: %w", err)
	}

	// Sandbox pod watcher, deliberately unfiltered by label since the pods are
	// operator created, then narrowed to the job namespaces in the handler
	podFactory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(a.dynamicClient, 0, "", nil)
	podInformer := podFactory.ForResource(podGVR).Informer()
	if err := podInformer.SetTransform(measurements.PodTransformFunc()); err != nil {
		log.Errorf("agenticRunLatency: failed to set Pod transform: %v", err)
	}
	if _, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: a.handleSandboxPod,
		UpdateFunc: func(oldObj, newObj any) {
			a.handleSandboxPod(newObj)
		},
	}); err != nil {
		return fmt.Errorf("error adding sandbox pod event handler: %w", err)
	}

	log.Infof("Starting agenticRunLatency watchers for job %s", a.JobConfig.Name)
	runFactory.Start(a.stopCh)
	podFactory.Start(a.stopCh)
	runFactory.WaitForCacheSync(a.stopCh)
	podFactory.WaitForCacheSync(a.stopCh)
	return nil
}

// Collect is a no-op, agenticRunLatency is informer driven
func (a *agenticRunLatency) Collect(measurementWg *sync.WaitGroup) {
	defer measurementWg.Done()
}

// Stop stops the agenticRunLatency measurement
func (a *agenticRunLatency) Stop() error {
	if a.JobConfig.SkipIndexing {
		return nil
	}
	close(a.stopCh)
	return a.StopMeasurement(a.normalizeMetrics, a.getLatency)
}

func (a *agenticRunLatency) GetMetrics() *sync.Map {
	return &a.Metrics
}

func (a *agenticRunLatency) normalizeMetrics() float64 {
	sandboxCount := map[string]int{}
	a.sandboxPods.Range(func(_, runUID any) bool {
		sandboxCount[runUID.(string)]++
		return true
	})

	var runCount, erroredRuns int
	a.Metrics.Range(func(key, value any) bool {
		m := value.(agenticRunMetric)
		runCount++
		m.SandboxPodCount = sandboxCount[key.(string)]
		// Cumulative latencies, measured from the AgenticRun creationTimestamp
		m.AnalyzedLatency = elapsed(m.Timestamp, m.analyzed, m.Name, latencyAnalyzed)
		m.ApprovedLatency = elapsed(m.Timestamp, m.approved, m.Name, latencyApproved)
		m.ExecutedLatency = elapsed(m.Timestamp, m.executed, m.Name, latencyExecuted)
		m.VerifiedLatency = elapsed(m.Timestamp, m.verified, m.Name, latencyVerified)
		m.EscalatedLatency = elapsed(m.Timestamp, m.escalated, m.Name, latencyEscalated)
		m.TerminalLatency = elapsed(m.Timestamp, m.terminal, m.Name, latencyTerminal)
		// Per-phase durations, each measured from the end of the preceding phase
		m.AnalysisPhaseLatency = elapsed(m.Timestamp, m.analyzed, m.Name, latencyAnalysisPhase)
		m.ApprovalPhaseLatency = elapsed(m.analyzed, m.approved, m.Name, latencyApprovalPhase)
		m.ExecutionPhaseLatency = elapsed(firstNonZero(m.approved, m.analyzed), m.executed, m.Name, latencyExecutionPhase)
		m.VerificationPhaseLatency = elapsed(m.executed, m.verified, m.Name, latencyVerificationPhase)
		m.EscalationPhaseLatency = elapsed(firstNonZero(m.verified, m.executed), m.escalated, m.Name, latencyEscalationPhase)
		m.SandboxStartupLatency = elapsed(m.sandboxFirst.created, m.sandboxFirst.ready, m.Name, latencySandboxStartup)
		m.SandboxStartupAnalysisLatency = elapsed(m.sandboxAnalysis.created, m.sandboxAnalysis.ready, m.Name, latencySandboxStartupAnalysis)
		m.SandboxStartupExecutionLatency = elapsed(m.sandboxExecution.created, m.sandboxExecution.ready, m.Name, latencySandboxStartupExecution)
		m.SandboxStartupVerificationLatency = elapsed(m.sandboxVerification.created, m.sandboxVerification.ready, m.Name, latencySandboxStartupVerification)
		m.SandboxStartupEscalationLatency = elapsed(m.sandboxEscalation.created, m.sandboxEscalation.ready, m.Name, latencySandboxStartupEscalation)
		m.ChurnMetric = a.IsChurnMetric(m.Timestamp)
		if m.terminal.IsZero() {
			log.Warningf("AgenticRun %s/%s did not reach a terminal phase, last derived phase: %s", m.Namespace, m.Name, m.Phase)
			erroredRuns++
		}
		a.NormLatencies = append(a.NormLatencies, m)
		return true
	})
	if runCount == 0 {
		return 0.0
	}
	return float64(erroredRuns) / float64(runCount) * 100.0
}

// getLatency only reports the series the run actually went through, a run that
// completes at Verified must not contribute a zero to the escalation quantiles
func (a *agenticRunLatency) getLatency(normLatency any) map[string]float64 {
	m := normLatency.(agenticRunMetric)
	latencies := map[string]float64{}
	observed := func(name string, reached time.Time, latency int) {
		if !reached.IsZero() {
			latencies[name] = float64(latency)
		}
	}
	observed(latencyAnalyzed, m.analyzed, m.AnalyzedLatency)
	observed(latencyApproved, m.approved, m.ApprovedLatency)
	observed(latencyExecuted, m.executed, m.ExecutedLatency)
	observed(latencyVerified, m.verified, m.VerifiedLatency)
	observed(latencyEscalated, m.escalated, m.EscalatedLatency)
	observed(latencyTerminal, m.terminal, m.TerminalLatency)
	observed(latencyAnalysisPhase, m.analyzed, m.AnalysisPhaseLatency)
	observed(latencyApprovalPhase, m.approved, m.ApprovalPhaseLatency)
	observed(latencyExecutionPhase, m.executed, m.ExecutionPhaseLatency)
	observed(latencyVerificationPhase, m.verified, m.VerificationPhaseLatency)
	observed(latencyEscalationPhase, m.escalated, m.EscalationPhaseLatency)
	observed(latencySandboxStartup, m.sandboxFirst.ready, m.SandboxStartupLatency)
	observed(latencySandboxStartupAnalysis, m.sandboxAnalysis.ready, m.SandboxStartupAnalysisLatency)
	observed(latencySandboxStartupExecution, m.sandboxExecution.ready, m.SandboxStartupExecutionLatency)
	observed(latencySandboxStartupVerification, m.sandboxVerification.ready, m.SandboxStartupVerificationLatency)
	observed(latencySandboxStartupEscalation, m.sandboxEscalation.ready, m.SandboxStartupEscalationLatency)
	return latencies
}

func (a *agenticRunLatency) IsCompatible() bool {
	_, exists := supportedAgenticRunLatencyJobTypes[a.JobConfig.JobType]
	return exists
}

// derivePhase mirrors the agentic-operator DerivePhase precedence:
// EmergencyStopped > Escalated > Denied > Verified > Executed > Analyzed.
// Phase is never stored on the CR, every consumer recomputes it (decision 0020).
func derivePhase(m *agenticRunMetric) string {
	switch {
	case !m.emergencyStopped.IsZero():
		return phaseEmergencyStopped
	case !m.escalated.IsZero():
		return phaseEscalated
	case !m.denied.IsZero():
		return phaseDenied
	case !m.verified.IsZero():
		return phaseCompleted
	case !m.failed.IsZero():
		return phaseFailed
	case !m.executed.IsZero():
		return phaseExecuted
	case !m.analyzed.IsZero():
		if m.AnalyzedReason == reasonNoActionRequired {
			return phaseNoActionRequired
		}
		return phaseProposed
	default:
		return phasePending
	}
}

// terminalTime returns the transition time of the terminal phase, or the zero
// value while the run is still progressing
func terminalTime(m *agenticRunMetric) time.Time {
	switch m.Phase {
	case phaseEmergencyStopped:
		return m.emergencyStopped
	case phaseEscalated:
		return m.escalated
	case phaseDenied:
		return m.denied
	case phaseCompleted:
		return m.verified
	case phaseFailed:
		return m.failed
	case phaseNoActionRequired:
		return m.analyzed
	default:
		return time.Time{}
	}
}

func firstNonZero(timestamps ...time.Time) time.Time {
	for _, timestamp := range timestamps {
		if !timestamp.IsZero() {
			return timestamp
		}
	}
	return time.Time{}
}

// elapsed returns the milliseconds between from and to, 0 when either end was
// not observed or when the interval is negative
func elapsed(from, to time.Time, name, series string) int {
	if from.IsZero() || to.IsZero() {
		return 0
	}
	latency := int(to.Sub(from).Milliseconds())
	if latency < 0 {
		log.Tracef("%s for AgenticRun %v is negative, explicitly setting it to 0", series, name)
		return 0
	}
	return latency
}

// intFromLabels reads a kube-burner numeric label, local equivalent of the
// upstream getIntFromLabels which is not exported
func intFromLabels(labels map[string]string, key string) int {
	value, err := strconv.Atoi(labels[key])
	if err != nil {
		return 0
	}
	return value
}

// agenticJobNamespaces computes the namespace set a job operates on, honoring
// namespacedIterations and iterationsPerNamespace. Local equivalent of the
// upstream getJobNamespaces which is not exported.
func agenticJobNamespaces(jobConfig *config.Job) map[string]struct{} {
	namespaces := map[string]struct{}{}
	if !jobConfig.NamespacedIterations {
		namespaces[jobConfig.Namespace] = struct{}{}
		return namespaces
	}
	iterationsPerNamespace := jobConfig.IterationsPerNamespace
	if iterationsPerNamespace < 1 {
		iterationsPerNamespace = 1
	}
	for i := range jobConfig.JobIterations {
		namespaces[fmt.Sprintf("%s-%d", jobConfig.Namespace, i/iterationsPerNamespace)] = struct{}{}
	}
	return namespaces
}

// agenticRunTransformFunc trims cached AgenticRun objects down to the fields
// the measurement reads, keeping informer memory flat at high CR counts:
// - metadata: name, namespace, uid, creationTimestamp, labels
// - status: conditions
func agenticRunTransformFunc() cache.TransformFunc {
	return func(obj any) (any, error) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return obj, nil
		}
		minimal := &unstructured.Unstructured{Object: map[string]any{}}
		minimal.SetAPIVersion(u.GetAPIVersion())
		minimal.SetKind(u.GetKind())
		minimal.SetName(u.GetName())
		minimal.SetNamespace(u.GetNamespace())
		minimal.SetUID(u.GetUID())
		minimal.SetCreationTimestamp(u.GetCreationTimestamp())
		minimal.SetLabels(u.GetLabels())
		if conditions, found, _ := unstructured.NestedSlice(u.Object, "status", "conditions"); found {
			_ = unstructured.SetNestedSlice(minimal.Object, conditions, "status", "conditions")
		}
		return minimal, nil
	}
}
