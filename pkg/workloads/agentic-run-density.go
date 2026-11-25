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

const defaultAgenticRunRequest = "Investigate the reported condition in openshift-lightspeed, propose a remediation and verify it."

var agenticWorkflows = []string{"analysis", "analysis-execution", "full"}
var agenticMockProfiles = []string{"trivial", "short", "typical", "long", "max-turns", "timeout", "malformed"}

var agenticMeasurementFactoryMap = map[string]kubeburnermeasurements.NewMeasurementFactory{
	"agenticRunLatency": measurements.NewAgenticRunLatencyMeasurementFactory,
}

// agenticOperatorNamespace is where the AgenticRuns have to be created, and it
// is pinned as a literal in agentic-run.yml for the reason documented there:
// the operator puts the sandbox pod and its input ConfigMap in its own
// namespace while owning them from the run, and a cross-namespace owner is
// collected immediately. The constant keeps the job namespace consistent
// with the AgenticRun template. Custom namespaces are not supported.
const agenticOperatorNamespace = "openshift-lightspeed"

// NewAgenticRunDensity holds the agentic-run-density workload, which drives
// concurrent OpenShift Lightspeed AgenticRun CRs through the agentic-operator
// and measures the per-phase latency of each run with the agenticRunLatency
// measurement.
func NewAgenticRunDensity(wh *workloads.WorkloadHelper) *cobra.Command {
	var iterations int
	var request, sandboxLabel, agent, workflow, mockProfile string
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

			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)
			AdditionalVars["JOB_ITERATIONS"] = iterations
			AdditionalVars["NAMESPACE"] = agenticOperatorNamespace
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
