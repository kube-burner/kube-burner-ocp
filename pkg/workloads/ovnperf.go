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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kubescheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

const (
	ovnkubeNamespaceName        = "openshift-ovn-kubernetes"
	ovnControllerContainerName  = "ovn-controller"
	ovnkubeNodePodLabelSelector = "app=ovnkube-node"
	workerNodeLabelSelector     = "node-role.kubernetes.io/worker="
	remoteArtifactParentPath    = "/var/log/ovn/kube-burner-perf"
	localArtifactDirectoryName  = "perf-data"
	ovnControllerProcessName    = "ovn-controller"
	ovnNorthdProcessName        = "ovn-northd"
)

type ovnOptions struct {
	OVNController bool
	OVNNorthd     bool
	Frequency     int
	Timeout       time.Duration
	UUID          string
}

type ovnPerfTarget struct {
	NodeName string
	PodName  string
}

type ovnPerfExecutor interface {
	Exec(ctx context.Context, namespace, pod, container string, command []string, stdout io.Writer) error
}

type ovnRemoteExecutor struct {
	clientSet  kubernetes.Interface
	restConfig *rest.Config
}

type ovnPerfCollector struct {
	executor   ovnPerfExecutor
	uuid       string
	podName    string
	frequency  int
	timeout    time.Duration
	components []string

	execContext context.Context
	execCancel  context.CancelFunc
	execDone    map[string]chan error
}

// Exec runs a command in a Kubernetes pod and captures its standard error.
func (e *ovnRemoteExecutor) Exec(ctx context.Context, namespace, pod, container string, command []string, stdout io.Writer) error {
	request := e.clientSet.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(pod).
		SubResource("exec")
	request.VersionedParams(&corev1.PodExecOptions{
		Command:   command,
		Container: container,
		Stdout:    true,
		Stderr:    true,
	}, kubescheme.ParameterCodec)

	remoteCommand, err := remotecommand.NewSPDYExecutor(e.restConfig, "POST", request.URL())
	if err != nil {
		return fmt.Errorf("failed to create pod executor: %v", err)
	}
	if stdout == nil {
		stdout = io.Discard
	}
	var stderr bytes.Buffer
	if err := remoteCommand.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: stdout,
		Stderr: &stderr,
	}); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("remote command failed: %w: %s", err, detail)
		}
		return fmt.Errorf("remote command failed: %w", err)
	}
	return nil
}

// addOVNPerfFlags registers profiling only on workloads that collect it.
func addOVNPerfFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("perf-ovn-controller", false, "Enable perf collection for ovn-controller")
	cmd.Flags().Bool("perf-ovn-northd", false, "Enable perf collection for ovn-northd")
	cmd.Flags().Int("perf-ovn-frequency", 99, "Perf sampling frequency in Hz")
}

// ovnOptionsFromCommand reads the enabled OVN perf flags.
func ovnOptionsFromCommand(cmd *cobra.Command, wh *workloads.WorkloadHelper) (ovnOptions, bool, error) {
	flags := cmd.Flags()
	options := ovnOptions{UUID: wh.UUID}
	var err error
	if options.OVNController, err = flags.GetBool("perf-ovn-controller"); err != nil {
		return options, false, err
	}
	if options.OVNNorthd, err = flags.GetBool("perf-ovn-northd"); err != nil {
		return options, false, err
	}
	if options.Frequency, err = flags.GetInt("perf-ovn-frequency"); err != nil {
		return options, false, err
	}
	if options.Timeout, err = cmd.Root().PersistentFlags().GetDuration("timeout"); err != nil {
		return options, false, err
	}
	if !options.OVNController && !options.OVNNorthd {
		return options, false, nil
	}
	if options.Frequency <= 0 {
		return options, false, errors.New("perf sampling frequency must be positive")
	}
	if options.UUID == "" || options.UUID == "." || options.UUID == ".." || strings.ContainsAny(options.UUID, `/\`) {
		return options, false, errors.New("perf UUID must be a nonempty single path component other than . or ..")
	}
	return options, true, nil
}

// selectWorkerPod chooses the first worker node with a running ovnkube-node pod.
func selectWorkerPod(ctx context.Context, clientSet kubernetes.Interface) (ovnPerfTarget, error) {
	nodes, err := clientSet.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: workerNodeLabelSelector,
	})
	if err != nil {
		return ovnPerfTarget{}, fmt.Errorf("failed to list worker nodes: %v", err)
	}
	sort.Slice(nodes.Items, func(i, j int) bool {
		return nodes.Items[i].Name < nodes.Items[j].Name
	})

	for _, node := range nodes.Items {
		pods, err := clientSet.CoreV1().Pods(ovnkubeNamespaceName).List(ctx, metav1.ListOptions{
			LabelSelector: ovnkubeNodePodLabelSelector,
			FieldSelector: "spec.nodeName=" + node.Name,
		})
		if err != nil {
			return ovnPerfTarget{}, fmt.Errorf("failed to list OVN pods on node %s: %v", node.Name, err)
		}
		sort.Slice(pods.Items, func(i, j int) bool {
			return pods.Items[i].Name < pods.Items[j].Name
		})
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				return ovnPerfTarget{NodeName: node.Name, PodName: pod.Name}, nil
			}
		}
	}
	return ovnPerfTarget{}, fmt.Errorf("no worker node has a running pod labeled %s", ovnkubeNodePodLabelSelector)
}

// startOVNPerfCollection initializes and starts the selected OVN perf collectors.
func startOVNPerfCollection(cmd *cobra.Command, wh *workloads.WorkloadHelper) (*ovnPerfCollector, error) {
	options, enabled, err := ovnOptionsFromCommand(cmd, wh)
	if err != nil || !enabled {
		return nil, err
	}

	kubeClientProvider := config.NewKubeClientProvider("", "")
	clientSet, restConfig := kubeClientProvider.DefaultClientSet()
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	target, err := selectWorkerPod(ctx, clientSet)
	if err != nil {
		return nil, err
	}

	collector := &ovnPerfCollector{
		executor:  &ovnRemoteExecutor{clientSet: clientSet, restConfig: restConfig},
		uuid:      options.UUID,
		podName:   target.PodName,
		frequency: options.Frequency,
		timeout:   options.Timeout,
	}
	if options.OVNController {
		collector.components = append(collector.components, ovnControllerProcessName)
	}
	if options.OVNNorthd {
		collector.components = append(collector.components, ovnNorthdProcessName)
	}

	ctx, cancel = context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	if err := collector.Start(ctx); err != nil {
		return nil, err
	}
	log.Infof("OVN perf collection started on worker %s pod %s for %s", target.NodeName, target.PodName, strings.Join(collector.components, ", "))
	return collector, nil
}

// Start launches one perf recording process for each selected OVN component.
func (c *ovnPerfCollector) Start(ctx context.Context) error {
	if c.execContext == nil {
		c.execContext, c.execCancel = context.WithCancel(context.Background())
	}
	c.execDone = make(map[string]chan error, len(c.components))

	attemptedComponents := []string{}
	for _, component := range c.components {
		cmd := append([]string{"/bin/bash", "-c", buildStartScript(), "kube-burner-perf"},
			c.uuid, component, strconv.Itoa(c.frequency), strconv.FormatInt(int64(c.timeout.Seconds()), 10))

		done := make(chan error, 1)
		c.execDone[component] = done
		go func() {
			defer close(done)
			done <- c.executor.Exec(c.execContext, ovnkubeNamespaceName, c.podName, ovnControllerContainerName, cmd, nil)
		}()

		attemptedComponents = append(attemptedComponents, component)
		if err := c.waitForStart(ctx, component, done); err != nil {
			c.abort(attemptedComponents)
			return fmt.Errorf("failed to start perf collection for %s: %v", component, err)
		}
	}
	return nil
}

func (c *ovnPerfCollector) waitForStart(ctx context.Context, component string, done chan error) error {
	pidPath := filepath.Join(remoteArtifactParentPath, c.uuid, component+".perf.pid")
	dataPath := filepath.Join(remoteArtifactParentPath, c.uuid, component+".perf.data")
	checkCommand := append([]string{"/bin/bash", "-c", buildCheckStartScript(), "kube-burner-perf"}, pidPath, dataPath)

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			return fmt.Errorf("perf process exited during startup: %v", err)
		case <-timeout:
			return errors.New("timed out waiting for perf process to start")
		case <-ticker.C:
			if err := c.executor.Exec(ctx, ovnkubeNamespaceName, c.podName, ovnControllerContainerName, checkCommand, nil); err == nil {
				select {
				case err := <-done:
					return fmt.Errorf("perf process exited during startup: %v", err)
				default:
				}
				return nil
			}
		}
	}
}

// buildCheckStartScript requires perf itself to be running and to have opened its output.
func buildCheckStartScript() string {
	return `set -euo pipefail
test -s "$1"
pid="$(cat "$1")"
kill -0 "$pid"
test "$(cat "/proc/${pid}/comm")" = "perf"
test -s "$2"
`
}

// abort stops every attempted collector and removes incomplete remote artifacts.
func (c *ovnPerfCollector) abort(components []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	defer c.execCancel()
	for _, component := range components {
		if err := c.stopComponent(ctx, component, false); err != nil {
			log.Errorf("Failed to abort perf collection for %s: %v", component, err)
		}
	}
	if len(components) > 0 {
		cleanupCommand := []string{"rm", "-rf", filepath.Join(remoteArtifactParentPath, c.uuid)}
		if err := c.executor.Exec(ctx, ovnkubeNamespaceName, c.podName, ovnControllerContainerName, cleanupCommand, nil); err != nil {
			log.Errorf("Failed to remove remote perf artifacts after aborted start for UUID %s: %v", c.uuid, err)
		}
	}
}

// buildStartScript returns the shell script that launches a component's perf recorder.
func buildStartScript() string {
	return `set -euo pipefail
root="` + remoteArtifactParentPath + `/$1"
component="$2"
frequency="$3"
timeout_seconds="$4"
mkdir -p "$root"
pid="$(pgrep -x "$component" | head -1 || true)"
if [[ -z "$pid" ]]; then
  echo "no ${component} process found on node" >&2
  exit 1
fi
printf '%s\n' "$pid" > "$root/${component}.target-pid"
printf '%s\n' "$$" > "$root/${component}.perf.pid"
exec perf record -F "$frequency" -g --call-graph dwarf -p "$pid" \
  -o "$root/${component}.perf.data" -- sleep "$timeout_seconds" \
  > "$root/${component}.perf.log" 2>&1
`
}

// buildStopScript returns the shared normal-stop and abort script for a component.
func buildStopScript() string {
	return `set -euo pipefail
root="` + remoteArtifactParentPath + `/$1"
component="$2"
generate_report="$3"
if [[ ! -s "$root/${component}.perf.pid" ]]; then
  exit 0
fi
target_pid="$(cat "$root/${component}.target-pid")"
perf_pid="$(cat "$root/${component}.perf.pid")"
if kill -0 "$perf_pid" 2>/dev/null; then
  kill -INT "$perf_pid"
  for _ in $(seq 1 120); do
    if ! kill -0 "$perf_pid" 2>/dev/null; then
      break
    fi
    sleep 1
  done
fi
if kill -0 "$perf_pid" 2>/dev/null; then
  kill -TERM "$perf_pid" 2>/dev/null || true
  sleep 5
  kill -KILL "$perf_pid" 2>/dev/null || true
  echo "perf process for ${component} did not stop after SIGINT" >&2
  exit 1
fi
if [[ "$generate_report" == "true" ]]; then
  perf report --symfs="/proc/${target_pid}/root" -i "$root/${component}.perf.data" \
    --stdio --sort dso,symbol > "$root/${component}.perf-report.txt" 2>> "$root/${component}.perf.log"
fi
`
}

// Stop generates reports, retrieves artifacts, and removes remote perf data.
func (c *ovnPerfCollector) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	var stopErr error
	for _, component := range c.components {
		if err := c.stopComponent(ctx, component, true); err != nil {
			log.Errorf("Failed to stop and report perf collection for %s: %v", component, err)
			stopErr = errors.Join(stopErr, err)
		}
	}

	if c.execCancel != nil {
		c.execCancel()
	}

	if err := c.retrieve(ctx); err != nil {
		log.Errorf("Failed to retrieve perf artifacts for UUID %s: %v", c.uuid, err)
		stopErr = errors.Join(stopErr, err)
	}
	if stopErr != nil {
		return stopErr
	}

	cleanupCommand := []string{"rm", "-rf", filepath.Join(remoteArtifactParentPath, c.uuid)}
	if err := c.executor.Exec(ctx, ovnkubeNamespaceName, c.podName, ovnControllerContainerName, cleanupCommand, nil); err != nil {
		log.Warnf("Failed to remove remote perf artifacts for UUID %s: %v", c.uuid, err)
	}
	return nil
}

func (c *ovnPerfCollector) stopComponent(ctx context.Context, component string, generateReport bool) error {
	cmd := append([]string{"/bin/bash", "-c", buildStopScript(), "kube-burner-perf"}, c.uuid, component, strconv.FormatBool(generateReport))
	if err := c.executor.Exec(ctx, ovnkubeNamespaceName, c.podName, ovnControllerContainerName, cmd, nil); err != nil {
		return err
	}

	done := c.execDone[component]
	if done == nil {
		return nil
	}
	select {
	case err := <-done:
		if err != nil {
			// The stop script sends SIGINT to perf. After writing its data, perf
			// terminates its workload with SIGTERM and then itself, producing 143.
			var exitErr utilexec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitStatus() == 143 {
				return nil
			}
			return fmt.Errorf("perf recording for %s failed: %w", component, err)
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		if c.execCancel != nil {
			c.execCancel()
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		return fmt.Errorf("timed out waiting for perf recording for %s to finish", component)
	}
	return nil
}

// retrieve downloads every known perf artifact for each selected component.
func (c *ovnPerfCollector) retrieve(ctx context.Context) error {
	artifactRoot := filepath.Join(localArtifactDirectoryName, c.uuid)
	if err := os.MkdirAll(artifactRoot, 0755); err != nil {
		return fmt.Errorf("failed to create local perf directory: %v", err)
	}

	var retrieveErr error
	for _, component := range c.components {
		for _, suffix := range []string{
			".perf.data",
			".perf-report.txt",
			".perf.log",
			".target-pid",
		} {
			remotePath := filepath.Join(remoteArtifactParentPath, c.uuid, component+suffix)
			localPath := filepath.Join(artifactRoot, component+suffix)
			if err := c.retrieveArtifact(ctx, remotePath, localPath); err != nil {
				retrieveErr = errors.Join(retrieveErr, err)
			}
		}
	}
	return retrieveErr
}

// retrieveArtifact copies one remote file to a local path with cat.
func (c *ovnPerfCollector) retrieveArtifact(ctx context.Context, remotePath, localPath string) error {
	file, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("failed to create %s: %v", localPath, err)
	}
	defer file.Close()
	cmd := append([]string{"/bin/bash", "-c", `cat "$1"`, "kube-burner-perf"}, remotePath)
	if err := c.executor.Exec(ctx, ovnkubeNamespaceName, c.podName, ovnControllerContainerName, cmd, file); err != nil {
		return fmt.Errorf("failed to retrieve %s: %v", remotePath, err)
	}
	return nil
}
