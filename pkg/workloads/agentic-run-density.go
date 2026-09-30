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

package workloads

import (
	"fmt"
	"os"
	"slices"
	"time"

	kubeburnermeasurements "github.com/kube-burner/kube-burner/v2/pkg/measurements"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	"github.com/spf13/cobra"

	"github.com/kube-burner/kube-burner-ocp/pkg/measurements"
)

const defaultAgenticRunRequest = "Investigate the reported condition in the target namespace, propose a remediation and verify it."
var agenticWorkflows = []string{"analysis", "analysis-execution", "full"}
var agenticMockProfiles = []string{"trivial", "short", "typical", "long", "max-turns", "timeout", "malformed"}

var agenticMeasurementFactoryMap = map[string]kubeburnermeasurements.NewMeasurementFactory{
	"agenticRunLatency": measurements.NewAgenticRunLatencyMeasurementFactory,
}

// agenticOperatorNamespace is where the AgenticRuns have to be created, and it
// is pinned as a literal in agentic-run.yml for the reason documented there:
// the operator puts the sandbox pod and its input ConfigMap in its own
// namespace while owning them from the run, and a cross-namespace owner is
// collected immediately. The constant exists so the Go side can refuse a
// --namespace that disagrees with the template rather than silently creating
// runs somewhere the template will override.
const agenticOperatorNamespace = "openshift-lightspeed"

// resolveTargetNamespaces decides the base name of spec.targetNamespaces and
// whether the workload has to create those namespaces itself.
func resolveTargetNamespaces(namespace, targetNamespace string, count *int) (string, bool) {
	if *count < 1 {
		*count = 1
	}
	switch {
	case targetNamespace == "":
		return namespace + "-target", true
	case targetNamespace == namespace:
		return namespace, false
	default:
		return targetNamespace, true
	}
}

// NewAgenticRunDensity holds the agentic-run-density workload, which drives
// concurrent OpenShift Lightspeed AgenticRun CRs through the agentic-operator
// and measures the per-phase latency of each run with the agenticRunLatency
// measurement.
func NewAgenticRunDensity(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations, targetNamespaceCount int
	var namespace, targetNamespace, request, sandboxLabel, agent, workflow, mockProfile string
	var runTimeout, iterationDelay time.Duration
	var waitForCompletion bool
	var metricsProfiles []string
	var rc int
	cmd := &cobra.Command{
		Use:   "agentic-run-density",
		Short: "Runs agentic-run-density workload",
		Long: "Creates AgenticRun CRs and measures how long the agentic-operator takes to drive them " +
			"through analysis, approval, execution and verification. Requires the agentic layer " +
			"(OCP >= 5.0) and an ApprovalPolicy granting automatic approval, otherwise every run " +
			"stalls waiting for a human reviewer. Run test/hack/deploy-mock-llm.sh first unless you " +
			"deliberately want to measure a real model.",
		SilenceUsage: true,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if !slices.Contains(agenticWorkflows, workflow) {
				return fmt.Errorf("invalid --workflow %q, must be one of %v", workflow, agenticWorkflows)
			}
			if mockProfile != "" && !slices.Contains(agenticMockProfiles, mockProfile) {
				return fmt.Errorf("invalid --mock-profile %q, must be empty or one of %v", mockProfile, agenticMockProfiles)
			}
			if namespace != agenticOperatorNamespace {
				return fmt.Errorf("--namespace must be %q: the operator creates sandbox pods in its own namespace "+
					"but owns them from the AgenticRun, and a cross-namespace owner is garbage collected immediately, "+
					"so every run fails to claim its sandbox", agenticOperatorNamespace)
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			targetBase, createTargets := resolveTargetNamespaces(namespace, targetNamespace, &targetNamespaceCount)
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["NAMESPACE"] = namespace
			AdditionalVars["TARGET_NAMESPACE"] = targetBase
			AdditionalVars["TARGET_NAMESPACE_COUNT"] = targetNamespaceCount
			AdditionalVars["CREATE_TARGET_NAMESPACES"] = createTargets
			AdditionalVars["REQUEST"] = request
			AdditionalVars["AGENT"] = agent
			AdditionalVars["WORKFLOW"] = workflow
			AdditionalVars["MOCK_PROFILE"] = mockProfile
			AdditionalVars["RUN_TIMEOUT"] = runTimeout
			AdditionalVars["ITERATION_DELAY"] = iterationDelay
			AdditionalVars["WAIT_FOR_COMPLETION"] = waitForCompletion
			measurements.SetAgenticRunSandboxLabel(sandboxLabel)
			wh.SetMeasurements(agenticMeasurementFactoryMap)
			rc = RunWorkload(cmd, wh, cmd.Name()+".yml")
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}
	cmd.Flags().IntVar(&iterations, "iterations", 0, "Number of AgenticRuns to create")
	cmd.Flags().StringVar(&namespace, "namespace", agenticOperatorNamespace, "Namespace the AgenticRuns are created in, which must be the agentic operator's own namespace")
	cmd.Flags().StringVar(&targetNamespace, "target-namespace", "", "Namespace the agent is scoped to and where execution RBAC is materialized, created by the workload. Defaults to <namespace>-target")
	cmd.Flags().IntVar(&targetNamespaceCount, "target-namespace-count", 1, "Spread the AgenticRuns across this many target namespaces, named <target-namespace>-0..N-1 when above 1")
	cmd.Flags().StringVar(&request, "request", defaultAgenticRunRequest, "Remediation request carried by every AgenticRun")
	cmd.Flags().StringVar(&agent, "agent", "default", "Agent CR every run step is bound to, must already exist")
	cmd.Flags().StringVar(&workflow, "workflow", "full", "Steps each run performs, one of analysis, analysis-execution, full. Omitted steps are skipped by the operator")
	cmd.Flags().StringVar(&mockProfile, "mock-profile", "typical", "Behavior to request from the mock LLM, one of "+
		"trivial, short, typical, long, max-turns, timeout, malformed. Injected into spec.request as a [mock-profile:...] "+
		"token, set to an empty string when running against a real model")
	cmd.Flags().DurationVar(&runTimeout, "run-timeout", 30*time.Minute, "How long to wait for an AgenticRun to reach a terminal phase")
	cmd.Flags().DurationVar(&iterationDelay, "iteration-delay", 0, "Delay between AgenticRun creations, used to pace the arrival rate")
	cmd.Flags().BoolVar(&waitForCompletion, "wait-for-completion", true, "Wait for every AgenticRun to reach a terminal phase before finishing the job")
	cmd.Flags().StringVar(&sandboxLabel, "sandbox-label", "", "Pod label key holding the AgenticRun UID, only needed when sandbox pods carry no ownerReference to their run")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"agentic-metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("iterations")
	return cmd
}
