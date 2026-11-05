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
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/cloud-bulldozer/go-commons/v2/indexers"
	"github.com/kube-burner/kube-burner/v2/pkg/config"
	"github.com/kube-burner/kube-burner/v2/pkg/prometheus"
	"github.com/kube-burner/kube-burner/v2/pkg/util/fileutils"
	"github.com/kube-burner/kube-burner/v2/pkg/util/metrics"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	fioResultMetricName    = "fioResult"
	fioCollectPollInterval = 10 * time.Second
	fioSSHWaitTimeout      = 10 * time.Minute
	fioSSHRunHeadroom      = 5 * time.Minute
)

// fioMetricsEmbedCfg loads metrics.yml from the same embed FS as RunWorkload
// (cmd/config/metrics-profiles/...), so post-fio scrapes do not need CWD files.
var fioMetricsEmbedCfg *fileutils.EmbedConfiguration

// InitFioMetricsEmbed stores embed FS paths for scrapeFioWindowMetrics.
// Call from cmd/ocp.go with the same embed used by NewWorkloadHelper.
func InitFioMetricsEmbed(embedFS *embed.FS, metricsDir, alertsDir string) {
	fioMetricsEmbedCfg = fileutils.NewEmbedConfiguration(embedFS, "", metricsDir, alertsDir, "")
}

// fioCollectOpts configures guest fio.json wait/copy/flatten/Index.
type fioCollectOpts struct {
	Namespace       string
	OS              string // linux | windows
	JobName         string
	ResultPath      string
	SSHUsername     string
	SSHPassword     string
	SSHIdentityFile string // private key path; preferred over password (virtctl password + sshpass is unreliable)
	Timeout         time.Duration
	StorageClass    string
	FioRuntime      string
	FioSize         string
	FioRw           string
	FioBs           string
	FioIoengine     string
	FioIodepth      string
	FioPath         string // Windows: path to fio.exe for SSH-started runs
}

type fioJobStats struct {
	Bw      float64   `json:"bw"`
	BwBytes float64   `json:"bw_bytes"`
	Iops    float64   `json:"iops"`
	LatNs   fioLatNs  `json:"lat_ns"`
	ClatNs  fioClatNs `json:"clat_ns"`
}

type fioLatNs struct {
	Mean float64 `json:"mean"`
}

type fioClatNs struct {
	Mean       float64            `json:"mean"`
	Percentile map[string]float64 `json:"percentile"`
}

type fioJob struct {
	JobName string      `json:"jobname"`
	Read    fioJobStats `json:"read"`
	Write   fioJobStats `json:"write"`
	Trim    fioJobStats `json:"trim"`
}

type fioReport struct {
	Jobs []fioJob `json:"jobs"`
}

func collectAndIndexFioResults(ctx context.Context, wh *workloads.WorkloadHelper, opts fioCollectOpts) error {
	if _, err := exec.LookPath("virtctl"); err != nil {
		return fmt.Errorf("virtctl is required to collect fio results: %w", err)
	}
	if opts.SSHIdentityFile == "" && opts.SSHPassword != "" {
		if _, err := exec.LookPath("sshpass"); err != nil {
			return fmt.Errorf("sshpass is required for password-based guest collect: %w", err)
		}
	}

	vmNames, err := listFioVMNames(ctx, opts.Namespace)
	if err != nil {
		return err
	}
	if len(vmNames) == 0 {
		return fmt.Errorf("no VirtualMachines found in namespace %s", opts.Namespace)
	}
	log.Infof("Collecting fio.json from %d VM(s) in %s (timeout %s)", len(vmNames), opts.Namespace, opts.Timeout)

	docs := make([]interface{}, 0, len(vmNames))
	var failed int
	for _, vmName := range vmNames {
		raw, node, err := waitAndReadFioJSON(ctx, opts, vmName)
		if err != nil {
			log.Errorf("Failed to collect fio.json from %s: %v", vmName, err)
			failed++
			continue
		}
		doc, err := flattenFioResult(raw, wh, opts, vmName, node)
		if err != nil {
			log.Errorf("Failed to flatten fio.json from %s: %v", vmName, err)
			failed++
			continue
		}
		docs = append(docs, doc)
		log.Infof("Collected fioResult for %s on node %s", vmName, node)
	}

	if len(docs) == 0 {
		return fmt.Errorf("collected no fio results (%d/%d VMs failed)", failed, len(vmNames))
	}
	if failed > 0 {
		log.Warnf("Collected fio results from %d/%d VMs (%d failed)", len(docs), len(vmNames), failed)
	}
	indexFioResults(docs)
	return nil
}

func listFioVMNames(ctx context.Context, namespace string) ([]string, error) {
	k8sConnector := getK8SConnector()
	vmGVR := schema.GroupVersionResource{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"}
	vmList, err := k8sConnector.DynamicClient().Resource(vmGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list VirtualMachines in %s: %w", namespace, err)
	}
	names := make([]string, 0, len(vmList.Items))
	for _, vm := range vmList.Items {
		names = append(names, vm.GetName())
	}
	return names, nil
}

func waitAndReadFioJSON(ctx context.Context, opts fioCollectOpts, vmName string) ([]byte, string, error) {
	deadline := time.Now().Add(opts.Timeout)
	var lastErr error
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		raw, err := readGuestFioJSON(ctx, opts, vmName)
		if err == nil {
			if json.Valid(raw) {
				node := getFioVMINode(ctx, opts.Namespace, vmName)
				return raw, node, nil
			}
			lastErr = fmt.Errorf("fio.json present but not valid JSON yet")
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return nil, "", fmt.Errorf("timeout waiting for %s on %s: %w", opts.ResultPath, vmName, lastErr)
		}
		attempt++
		// Info every ~60s so long waits do not look hung (poll is 10s).
		if attempt == 1 || attempt%6 == 0 {
			log.Infof("Still waiting for %s on %s (%s left): %v", opts.ResultPath, vmName, time.Until(deadline).Round(time.Second), lastErr)
		} else {
			log.Debugf("Waiting for fio.json on %s: %v", vmName, lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(fioCollectPollInterval):
		}
	}
}

// waitForFioSSH polls until virtctl ssh succeeds for every VM in the namespace.
// Used after jobPause=0 so fio starts only once the guest is reachable (BR winfio style).
func waitForFioSSH(ctx context.Context, opts fioCollectOpts, timeout time.Duration) error {
	if _, err := exec.LookPath("virtctl"); err != nil {
		return fmt.Errorf("virtctl is required to wait for guest SSH: %w", err)
	}
	if opts.SSHIdentityFile == "" && opts.SSHPassword != "" {
		if _, err := exec.LookPath("sshpass"); err != nil {
			return fmt.Errorf("sshpass is required for password-based guest SSH: %w", err)
		}
	}
	vmNames, err := listFioVMNames(ctx, opts.Namespace)
	if err != nil {
		return err
	}
	if len(vmNames) == 0 {
		return fmt.Errorf("no VirtualMachines found in namespace %s", opts.Namespace)
	}
	log.Infof("Waiting for SSH on %d VM(s) in %s (timeout %s)", len(vmNames), opts.Namespace, timeout)
	deadline := time.Now().Add(timeout)
	attempt := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		allOK := true
		var lastErr error
		for _, vmName := range vmNames {
			out, err := virtctlSSHCommand(ctx, opts, vmName, "echo fio-ssh-ok")
			if err != nil || !strings.Contains(string(out), "fio-ssh-ok") {
				allOK = false
				if err != nil {
					lastErr = err
				} else {
					lastErr = fmt.Errorf("unexpected SSH output: %q", strings.TrimSpace(string(out)))
				}
				break
			}
		}
		if allOK {
			log.Infof("SSH ready on all %d VM(s) in %s", len(vmNames), opts.Namespace)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for SSH in %s: %w", opts.Namespace, lastErr)
		}
		attempt++
		if attempt == 1 || attempt%6 == 0 {
			log.Infof("Still waiting for SSH in %s (%s left): %v", opts.Namespace, time.Until(deadline).Round(time.Second), lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(fioCollectPollInterval):
		}
	}
}

// runWindowsFioViaSSH starts fio on every VM in parallel (BR winfio style: orchestrator owns the run).
func runWindowsFioViaSSH(ctx context.Context, opts fioCollectOpts) error {
	if opts.FioPath == "" {
		return fmt.Errorf("FioPath is required to start Windows fio via SSH")
	}
	// Do not use --directory=C:/testdir: Windows fio still splits directory= on ':' .
	command := fmt.Sprintf(
		`powershell -NoProfile -ExecutionPolicy Bypass -Command "New-Item -ItemType Directory -Force -Path 'C:/testdir' | Out-Null; Set-Location C:/testdir; & '%s' --name=fio-vmwindows --filename=fio.bin --rw=%s --bs=%s --size=%s --ioengine=%s --iodepth=%s --direct=1 --thread --time_based --runtime=%s --output-format=json --output=C:/testdir/fio.json 2> C:/testdir/fio.err"`,
		opts.FioPath, opts.FioRw, opts.FioBs, opts.FioSize, opts.FioIoengine, opts.FioIodepth, opts.FioRuntime,
	)
	return runFioViaSSHParallel(ctx, opts, command)
}

// runLinuxFioViaSSH starts fio on every Linux VM in parallel after SSH is up.
func runLinuxFioViaSSH(ctx context.Context, opts fioCollectOpts) error {
	// Ensure fio exists (cloud-init packages may still be finishing when SSH first answers).
	command := fmt.Sprintf(
		`bash -c 'command -v fio >/dev/null 2>&1 || dnf install -y fio; mkdir -p /root/testdir; fio --name=fio-vmlinux --directory=/root/testdir --rw=%s --bs=%s --size=%s --ioengine=%s --iodepth=%s --direct=1 --time_based --runtime=%s --output-format=json --output=/root/testdir/fio.json 2>/root/testdir/fio.err'`,
		opts.FioRw, opts.FioBs, opts.FioSize, opts.FioIoengine, opts.FioIodepth, opts.FioRuntime,
	)
	return runFioViaSSHParallel(ctx, opts, command)
}

func runFioViaSSHParallel(ctx context.Context, opts fioCollectOpts, command string) error {
	vmNames, err := listFioVMNames(ctx, opts.Namespace)
	if err != nil {
		return err
	}
	if len(vmNames) == 0 {
		return fmt.Errorf("no VirtualMachines found in namespace %s", opts.Namespace)
	}

	runTimeout := fioSSHRunHeadroom
	if d, err := time.ParseDuration(opts.FioRuntime); err == nil {
		runTimeout = d + fioSSHRunHeadroom
	}
	log.Infof("Starting fio via SSH on %d VM(s) in %s (per-VM timeout %s, runtime %s)",
		len(vmNames), opts.Namespace, runTimeout, opts.FioRuntime)

	var wg sync.WaitGroup
	errCh := make(chan error, len(vmNames))
	for _, vmName := range vmNames {
		wg.Add(1)
		go func(vmName string) {
			defer wg.Done()
			runCtx, cancel := context.WithTimeout(ctx, runTimeout)
			defer cancel()
			log.Infof("Starting fio on %s via SSH", vmName)
			out, err := virtctlSSHCommand(runCtx, opts, vmName, command)
			if err != nil {
				errCh <- fmt.Errorf("%s: %w: %s", vmName, err, strings.TrimSpace(string(out)))
				return
			}
			log.Infof("fio SSH command finished on %s", vmName)
		}(vmName)
	}
	wg.Wait()
	close(errCh)

	var failed []string
	for err := range errCh {
		failed = append(failed, err.Error())
		log.Errorf("fio via SSH failed: %v", err)
	}
	if len(failed) == len(vmNames) {
		return fmt.Errorf("fio via SSH failed on all %d VM(s)", len(vmNames))
	}
	if len(failed) > 0 {
		log.Warnf("fio via SSH failed on %d/%d VM(s); continuing to collect", len(failed), len(vmNames))
	}
	return nil
}

func readGuestFioJSON(ctx context.Context, opts fioCollectOpts, vmName string) ([]byte, error) {
	var command string
	if opts.OS == "windows" {
		// PowerShell Get-Content so path colons are fine.
		command = fmt.Sprintf(`powershell -NoProfile -Command "Get-Content -Raw '%s'"`, opts.ResultPath)
	} else {
		command = fmt.Sprintf("cat %s", opts.ResultPath)
	}

	out, err := virtctlSSHCommand(ctx, opts, vmName, command)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, fmt.Errorf("empty output from guest")
	}
	// Strip possible SSH banner noise: find first '{'
	if idx := strings.Index(trimmed, "{"); idx >= 0 {
		trimmed = trimmed[idx:]
	}
	return []byte(trimmed), nil
}

// virtctlSSHCommand runs a remote command via virtctl ssh.
// Use --command= (not -c). Do not pass --local-ssh (absent on virtctl 1.9+).
func virtctlSSHCommand(ctx context.Context, opts fioCollectOpts, vmName, command string) ([]byte, error) {
	virtctlArgs := []string{
		"ssh",
		"--local-ssh-opts=-o StrictHostKeyChecking=no",
		"--local-ssh-opts=-o UserKnownHostsFile=/dev/null",
		"--local-ssh-opts=-o ConnectTimeout=5",
	}
	useKey := opts.SSHIdentityFile != ""
	if useKey {
		virtctlArgs = append(virtctlArgs,
			"--identity-file="+opts.SSHIdentityFile,
			"--local-ssh-opts=-o BatchMode=yes",
			"--local-ssh-opts=-o PasswordAuthentication=no",
			"--local-ssh-opts=-o IdentitiesOnly=yes",
		)
	}
	virtctlArgs = append(virtctlArgs,
		"-n", opts.Namespace,
		"--command="+command,
		fmt.Sprintf("%s@vmi/%s", opts.SSHUsername, vmName),
	)

	var cmd *exec.Cmd
	if !useKey && opts.SSHPassword != "" {
		// Prefer SSH keys: sshpass often cannot drive virtctl's password prompt (exit 5 / Permission denied).
		args := append([]string{"-p", opts.SSHPassword, "virtctl"}, virtctlArgs...)
		cmd = exec.CommandContext(ctx, "sshpass", args...)
	} else {
		cmd = exec.CommandContext(ctx, "virtctl", virtctlArgs...)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func getFioVMINode(ctx context.Context, namespace, vmName string) string {
	output, err := exec.CommandContext(ctx, "kubectl", "get", "vmi", vmName, "-n", namespace,
		"-o", "jsonpath={.status.nodeName}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func flattenFioResult(raw []byte, wh *workloads.WorkloadHelper, opts fioCollectOpts, vmName, node string) (map[string]any, error) {
	var report fioReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, err
	}
	if len(report.Jobs) == 0 {
		return nil, fmt.Errorf("fio report has no jobs")
	}
	job := report.Jobs[0]
	stats := pickFioStats(job, opts.FioRw)

	p99 := percentileFrom(stats.ClatNs.Percentile, "99.000000", "99.0", "99")
	doc := map[string]any{
		"uuid":         AdditionalVars["UUID"],
		"metricName":   fioResultMetricName,
		"jobName":      opts.JobName,
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"metadata":     wh.MetricsMetadata,
		"vm_name":      vmName,
		"namespace":    opts.Namespace,
		"node":         node,
		"os":           opts.OS,
		"storageClass": opts.StorageClass,
		"fio_runtime":  opts.FioRuntime,
		"fio_size":     opts.FioSize,
		"fio_rw":       opts.FioRw,
		"fio_bs":       opts.FioBs,
		"fio_ioengine": opts.FioIoengine,
		"fio_iodepth":  opts.FioIodepth,
		"fio_jobname":  job.JobName,
		"bw":           stats.Bw,
		"bw_bytes":     stats.BwBytes,
		"iops":         stats.Iops,
		"lat_ns_mean":  stats.LatNs.Mean,
		"clat_ns_mean": stats.ClatNs.Mean,
		"clat_ns_p99":  p99,
	}
	return doc, nil
}

func pickFioStats(job fioJob, rw string) fioJobStats {
	switch {
	case strings.Contains(rw, "read") && !strings.Contains(rw, "write"):
		return job.Read
	case strings.Contains(rw, "trim"):
		return job.Trim
	case strings.Contains(rw, "rw") || strings.Contains(rw, "randrw"):
		// Prefer write for mixed; both present in fio JSON.
		if job.Write.Iops > 0 || job.Write.BwBytes > 0 {
			return job.Write
		}
		return job.Read
	default:
		if job.Write.Iops > 0 || job.Write.BwBytes > 0 || job.Write.Bw > 0 {
			return job.Write
		}
		if job.Read.Iops > 0 || job.Read.BwBytes > 0 || job.Read.Bw > 0 {
			return job.Read
		}
		return job.Write
	}
}

func percentileFrom(m map[string]float64, keys ...string) float64 {
	if m == nil {
		return 0
	}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return 0
}

func indexFioResults(docs []interface{}) {
	for _, endpoint := range workloads.ConfigSpec.MetricsEndpoints {
		if endpoint.Type == "" {
			continue
		}
		idx, err := indexers.NewIndexer(endpoint.IndexerConfig)
		if err != nil {
			log.Errorf("Failed to create indexer for fioResult: %v", err)
			continue
		}
		resp, err := (*idx).Index(docs, indexers.IndexingOpts{
			MetricName: fioResultMetricName,
		})
		if err != nil {
			log.Errorf("Failed to index fioResult docs: %v", err)
		} else {
			log.Infof("fioResult indexed: %s", resp)
		}
	}
}

func fioCollectTimeout(fioRuntime string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(fioRuntime)
	if err != nil {
		return fallback
	}
	// Boot/cloud-init headroom + fio runtime + small buffer.
	return d + fallback
}

// normalizeFioList flattens CSV tokens from a StringSlice flag, trims, drops empties,
// and dedupes while preserving first-seen order.
func normalizeFioList(values []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(values))
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, ok := seen[part]; ok {
				continue
			}
			seen[part] = struct{}{}
			out = append(out, part)
		}
	}
	return out
}

type fioCombo struct {
	Bs string
	Rw string
}

// fioCombos builds bs × rw Cartesian product (bs outer, rw inner), preserving flag order.
func fioCombos(bsList, rwList []string) []fioCombo {
	bsList = normalizeFioList(bsList)
	rwList = normalizeFioList(rwList)
	if len(bsList) == 0 {
		bsList = []string{"4k"}
	}
	if len(rwList) == 0 {
		rwList = []string{"write"}
	}
	out := make([]fioCombo, 0, len(bsList)*len(rwList))
	for _, bs := range bsList {
		for _, rw := range rwList {
			out = append(out, fioCombo{Bs: bs, Rw: rw})
		}
	}
	return out
}

func fioJobNameSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, s)
}

func isWriteLikeFioRw(rw string) bool {
	switch strings.ToLower(strings.TrimSpace(rw)) {
	case "read", "randread", "trim":
		return false
	default:
		return true
	}
}

type fioStartFunc func(ctx context.Context, opts fioCollectOpts) error

// prepareGuestFioDataFile deletes the guest data file before write-like combos so a prior
// size/content does not skew the next write. Skipped for pure read/randread/trim.
func prepareGuestFioDataFile(ctx context.Context, opts fioCollectOpts) {
	if !isWriteLikeFioRw(opts.FioRw) {
		return
	}
	vmNames, err := listFioVMNames(ctx, opts.Namespace)
	if err != nil {
		log.Warnf("Skipping guest data-file prep: list VMs: %v", err)
		return
	}
	var command string
	if opts.OS == "windows" {
		command = `powershell -NoProfile -ExecutionPolicy Bypass -Command "Remove-Item -Force -ErrorAction SilentlyContinue 'C:/testdir/fio.bin'"`
	} else {
		command = `bash -c 'rm -f /root/testdir/fio-vmlinux /root/testdir/fio-vmlinux.*'`
	}
	for _, vmName := range vmNames {
		if out, err := virtctlSSHCommand(ctx, opts, vmName, command); err != nil {
			log.Warnf("Guest data-file prep on %s: %v: %s", vmName, err, strings.TrimSpace(string(out)))
		}
	}
}

// runFioMatrix runs sequential (bs × rw) combos on already-created VMs: prep → start →
// Prom scrape → collect/Index. Parallelism across VMs is unchanged inside each combo.
func runFioMatrix(ctx context.Context, wh *workloads.WorkloadHelper, baseOpts fioCollectOpts, bsList, rwList []string, startFn fioStartFunc, scrapeJobPrefix string, collect bool, collectTimeout time.Duration) error {
	combos := fioCombos(bsList, rwList)
	log.Infof("fio matrix: %d combo(s) (bs × rw)", len(combos))
	var failed []string
	for i, combo := range combos {
		opts := baseOpts
		opts.FioBs = combo.Bs
		opts.FioRw = combo.Rw
		jobName := fmt.Sprintf("%s-%s-%s", scrapeJobPrefix, fioJobNameSafe(combo.Bs), fioJobNameSafe(combo.Rw))
		log.Infof("fio matrix [%d/%d] bs=%s rw=%s jobName=%s", i+1, len(combos), combo.Bs, combo.Rw, jobName)

		prepareGuestFioDataFile(ctx, opts)

		fioStart := time.Now()
		if err := startFn(ctx, opts); err != nil {
			log.Errorf("fio matrix combo bs=%s rw=%s start failed: %v", combo.Bs, combo.Rw, err)
			failed = append(failed, fmt.Sprintf("%s/%s: start: %v", combo.Bs, combo.Rw, err))
			continue
		}
		scrapeFioWindowMetrics(wh, jobName, fioStart, time.Now())
		if collect {
			opts.Timeout = collectTimeout
			if err := collectAndIndexFioResults(ctx, wh, opts); err != nil {
				log.Errorf("fio matrix combo bs=%s rw=%s collect failed: %v", combo.Bs, combo.Rw, err)
				failed = append(failed, fmt.Sprintf("%s/%s: collect: %v", combo.Bs, combo.Rw, err))
				continue
			}
		}
	}
	if len(failed) == len(combos) {
		return fmt.Errorf("fio matrix failed on all %d combo(s): %s", len(combos), strings.Join(failed, "; "))
	}
	if len(failed) > 0 {
		log.Warnf("fio matrix failed on %d/%d combo(s): %s", len(failed), len(combos), strings.Join(failed, "; "))
	}
	return nil
}

// scrapeFioWindowMetrics runs a second Prometheus scrape for [start, end] so node/cluster
// metrics cover the SSH-started fio phase (RunWorkload scrape ends before that phase).
func scrapeFioWindowMetrics(wh *workloads.WorkloadHelper, jobName string, start, end time.Time) {
	if len(workloads.ConfigSpec.MetricsEndpoints) == 0 {
		log.Info("No metrics endpoints configured; skipping fio-window Prometheus scrape")
		return
	}
	if fioMetricsEmbedCfg == nil {
		log.Warn("InitFioMetricsEmbed not called; skipping fio-window Prometheus scrape")
		return
	}
	if !end.After(start) {
		log.Warnf("Skipping fio-window Prometheus scrape: invalid range %s → %s", start, end)
		return
	}
	log.Infof("Scraping Prometheus for fio window job=%s start=%s end=%s",
		jobName, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
	metricsScraper := metrics.ProcessMetricsScraperConfig(metrics.ScraperConfig{
		ConfigSpec:      &workloads.ConfigSpec,
		MetricsEndpoint: wh.MetricsEndpoint,
		MetricsMetadata: wh.MetricsMetadata,
		SummaryMetadata: wh.SummaryMetadata,
		EmbedCfg:        fioMetricsEmbedCfg,
	})
	for _, prometheusClient := range metricsScraper.PrometheusClients {
		prometheusJob := prometheus.Job{
			Start: start,
			End:   end,
			JobConfig: config.Job{
				Name: jobName,
			},
		}
		if err := prometheusClient.ScrapeJobsMetrics(prometheusJob); err != nil {
			log.Errorf("fio-window Prometheus scrape failed for %s: %v", jobName, err)
		}
	}
}

func shouldCleanupAfterFioCollect(userGC any) bool {
	switch v := userGC.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return true
	}
}
