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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloud-bulldozer/go-commons/v2/indexers"
	"github.com/cloud-bulldozer/go-commons/v2/podfiles"
	uid "github.com/google/uuid"
	kbmetrics "github.com/kube-burner/kube-burner/v2/pkg/util/metrics"
	"github.com/kube-burner/kube-burner/v2/pkg/workloads"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	enmaasAppLabel    = "app=enmaas-loadtest"
	enmaasUUIDLabel   = "enmaas-loadtest.kube-burner.io/uuid"
	enmaasModelLabel  = "enmaas-loadtest.kube-burner.io/model"
	enmaasTargetLabel = "enmaas-loadtest.kube-burner.io/target"
	enmaasJobLabel    = "enmaas-loadtest.kube-burner.io/job"
	enmaasContainer   = "loadtest"

	enmaasConcurrencyLabel    = "enmaas-loadtest.kube-burner.io/concurrency"
	enmaasResultsDir          = "/results"
	enmaasKubeBurnerUUIDLabel = "kube-burner.io/uuid"
	enmaasSweepInterval       = 5 * time.Second

	enmaasLevelMetric = "enmaasLoadtestLevel"

	enmaasDefaultImage   = "quay.io/vchalla/enmaas-loadtest:latest"
	enmaasDefaultScript  = "loadtest.py"
	enmaasKeySecretName  = "enmaas-loadtest-keys"
	enmaasKeySecretEntry = "api-key.txt"

	enmaasMintUsersPath   = "/api/v1/users"
	enmaasMeteringService = "metering-service"
	enmaasMintHTTPWait    = 30 * time.Second

	enmaasDeadlineFactor = 3
	enmaasDeadlineGrace  = 10 * time.Minute
)

// enmaasJobDeadline calculates the activeDeadlineSeconds for a Job based on the number of
// concurrency levels, samples, parallelism, and duration of each run.
func enmaasJobDeadline(levelCount, samples, parallelism int, duration time.Duration) time.Duration {
	if levelCount < 1 {
		levelCount = 1
	}
	if samples < 1 {
		samples = 1
	}
	if parallelism < 1 {
		parallelism = 1
	}
	batches := (samples + parallelism - 1) / parallelism
	perRun := time.Duration(levelCount) * duration
	return time.Duration(batches)*perRun*enmaasDeadlineFactor + enmaasDeadlineGrace
}

// parseEnmaasLevels validates the comma separated concurrency list and returns
// how many levels it contains.
func parseEnmaasLevels(levels string) (int, error) {
	fields := strings.Split(levels, ",")
	count := 0
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		value, err := strconv.Atoi(field)
		if err != nil {
			return 0, fmt.Errorf("%q is not an integer", field)
		}
		if value < 1 {
			return 0, fmt.Errorf("%d is not a positive concurrency level", value)
		}
		count++
	}
	if count == 0 {
		return 0, fmt.Errorf("no concurrency levels given")
	}
	return count, nil
}

// readEnmaasSecretFile reads a credential file and returns it base64 encoded for
// embedding in the Secret object template.
func readEnmaasSecretFile(path, what string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("Failed to read %s from %q: %v", what, path, err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		log.Fatalf("%s file %q is empty", what, path)
	}
	return base64.StdEncoding.EncodeToString([]byte(trimmed))
}

// readEnmaasSecretRef pulls a credential out of an existing Secret, given as
// <namespace>/<name>, and returns it base64 encoded.
func readEnmaasSecretRef(ref, key, what string) string {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		log.Fatalf("Invalid %s secret reference %q: expected <namespace>/<name>", what, ref)
	}
	namespace, name := parts[0], parts[1]

	k8s := getK8SConnector()
	secret, err := k8s.ClientSet().CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		log.Fatalf("Failed to read %s from Secret %s/%s: %v", what, namespace, name, err)
	}
	value, ok := secret.Data[key]
	if !ok {
		available := make([]string, 0, len(secret.Data))
		for k := range secret.Data {
			available = append(available, k)
		}
		sort.Strings(available)
		log.Fatalf("Secret %s/%s has no key %q (available keys: %s)",
			namespace, name, key, strings.Join(available, ", "))
	}
	trimmed := strings.TrimSpace(string(value))
	if trimmed == "" {
		log.Fatalf("Key %q in Secret %s/%s is empty", key, namespace, name)
	}
	log.Infof("Using %s from Secret %s/%s key %q", what, namespace, name, key)
	return base64.StdEncoding.EncodeToString([]byte(trimmed))
}

// resolveEnmaasCredential accepts exactly one of a literal value, a local file
// or an existing Secret reference, and returns the value base64 encoded. The
// caller turns that into a Secret in the test namespace.
func resolveEnmaasCredential(literal, file, secretRef, secretKey, flagPrefix, what string, required bool) string {
	switch {
	case literal != "":
		return base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(literal)))
	case file != "":
		return readEnmaasSecretFile(file, what)
	case secretRef != "":
		return readEnmaasSecretRef(secretRef, secretKey, what)
	case required:
		log.Fatalf("One of --%s, --%s-file or --%s-secret is required", flagPrefix, flagPrefix, flagPrefix)
	}
	return ""
}

// validateEnmaasURLs checks the benchmark targets and returns the gateway
// hostname, which hostAliases needs when --gateway-ip is given.
func validateEnmaasURLs(gatewayURL, baselineURL string) string {
	parsed, err := url.Parse(gatewayURL)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		log.Fatalf("Invalid --url %q: expected a full URL including scheme and host, "+
			"e.g. https://api.enmaas.devshift.net/v1/chat/completions", gatewayURL)
	}
	if baselineURL != "" {
		base, err := url.Parse(baselineURL)
		if err != nil || base.Scheme == "" || base.Hostname() == "" {
			log.Fatalf("Invalid --baseline-url %q: expected a full URL including scheme and host", baselineURL)
		}
	}
	return parsed.Hostname()
}

// parseEnmaasModels splits and validates the comma separated model list.
func parseEnmaasModels(models string) []string {
	parsed := strings.Split(models, ",")
	for i, model := range parsed {
		parsed[i] = strings.TrimSpace(model)
		if parsed[i] == "" {
			log.Fatalf("Invalid --models %q: contains an empty entry", models)
		}
	}
	if len(parsed) == 0 {
		log.Fatal("--models must list at least one model")
	}
	return parsed
}

// mintEnmaasRunKey creates the throwaway identity used for one run.
func mintEnmaasRunKey(ctx context.Context, meteringURL, gatewayURL,
	namespace, tokenSecret, tokenSecretKey string) (*enmaasMeteringClient, *enmaasMintedKey) {
	log.Warnf("Minting a throwaway EnMaaS user: minted identities pollute usage and "+
		"active-user analytics, so prefer this on stage only. Target: %s", gatewayURL)

	metering := newEnmaasMeteringClient(meteringURL, readEnmaasMeteringToken(namespace, tokenSecret, tokenSecretKey))
	minted, err := metering.mintKey(ctx)
	if err != nil {
		log.Fatalf("Failed to mint an API key via %s: %v "+
			"(from outside the cluster, port-forward the metering service and pass "+
			"--metering-url=http://localhost:18081)", meteringURL, err)
	}
	log.Infof("Minted API key for throwaway user %s", minted.Username)
	return metering, minted
}

// enmaasMintedKey is a throwaway identity created for one benchmark run.
type enmaasMintedKey struct {
	Username string
	Key      string
}

// enmaasMeteringClient talks to the metering user-management API, which owns
// EnMaaS users and their API keys.
type enmaasMeteringClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func newEnmaasMeteringClient(baseURL, token string) *enmaasMeteringClient {
	return &enmaasMeteringClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
		client:  &http.Client{Timeout: enmaasMintHTTPWait},
	}
}

func (c *enmaasMeteringClient) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s returned %d: %s",
			method, path, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	return payload, nil
}

// mintKey creates a throwaway user and issues it an API key, mirroring
// EnMaaS-opps scripts/smoke_test.py.
func (c *enmaasMeteringClient) mintKey(ctx context.Context) (*enmaasMintedKey, error) {
	username := uid.NewString()
	userBody, err := json.Marshal(map[string]any{
		"user_id": username,
		"tags": map[string]string{
			"email":      username + "@enmaas-loadtest.invalid",
			"first_name": "Load",
			"last_name":  "Test",
			"origin":     "loadtest",
		},
	})
	if err != nil {
		return nil, err
	}
	if _, err := c.do(ctx, http.MethodPost, enmaasMintUsersPath, userBody); err != nil {
		return nil, fmt.Errorf("creating user: %w", err)
	}

	keyBody := []byte(`{"name":"kube-burner-ocp-enmaas-loadtest"}`)
	raw, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("%s/%s/keys", enmaasMintUsersPath, username), keyBody)
	if err != nil {
		// The user exists but has no key, so clean it up rather than leaking it.
		if revokeErr := c.revokeUser(ctx, username); revokeErr != nil {
			log.Warnf("Failed to clean up user %s after a failed key mint: %v", username, revokeErr)
		}
		return nil, fmt.Errorf("creating key: %w", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decoding key response: %w", err)
	}
	key, _ := decoded["key"].(string)
	if key == "" {
		key, _ = decoded["id"].(string)
	}
	if key == "" {
		return nil, fmt.Errorf("key response contained neither \"key\" nor \"id\"")
	}
	return &enmaasMintedKey{Username: username, Key: key}, nil
}

// revokeUser soft-deletes the throwaway user, which deactivates its keys.
func (c *enmaasMeteringClient) revokeUser(ctx context.Context, username string) error {
	_, err := c.do(ctx, http.MethodDelete, enmaasMintUsersPath+"/"+username, nil)
	return err
}

// readEnmaasMeteringToken pulls the user-management API token out of its Secret.
func readEnmaasMeteringToken(namespace, name, key string) string {
	k8s := getK8SConnector()
	secret, err := k8s.ClientSet().CoreV1().Secrets(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		log.Fatalf("Failed to read the metering API token from Secret %s/%s: %v", namespace, name, err)
	}
	value, ok := secret.Data[key]
	if !ok {
		log.Fatalf("Secret %s/%s has no key %q", namespace, name, key)
	}
	token := strings.TrimSpace(string(value))
	if token == "" {
		log.Fatalf("Key %q in Secret %s/%s is empty", key, namespace, name)
	}
	return token
}

// enmaasRunConfig is the run's configuration, known to the wrapper from its
// own flags rather than scraped back out of the pod.
type enmaasRunConfig struct {
	URLs            map[string]string
	DurationSeconds int
	MaxTokens       int
	MaxKey          string
	TTFT            bool
	ScriptName      string
}

// enmaasCollector pulls result files off the load generator pods.
type enmaasCollector struct {
	namespace string
	uuid      string
	cfg       enmaasRunConfig
	metadata  map[string]any

	mu        sync.Mutex
	collected map[string][]any
}

func newEnmaasCollector(namespace, uuid string, cfg enmaasRunConfig, metadata map[string]any) *enmaasCollector {
	return &enmaasCollector{
		namespace: namespace,
		uuid:      uuid,
		cfg:       cfg,
		metadata:  metadata,
		collected: make(map[string][]any),
	}
}

// watch sweeps for running pods until the context is canceled.
func (c *enmaasCollector) watch(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.sweep(context.WithoutCancel(ctx))
			return
		case <-ticker.C:
			c.sweep(ctx)
		}
	}
}

// sweep collects from every running pod not yet collected from.
func (c *enmaasCollector) sweep(ctx context.Context) {
	k8s := getK8SConnector()
	selector := fmt.Sprintf("%s,%s=%s", enmaasAppLabel, enmaasUUIDLabel, c.uuid)
	pods, err := k8s.ClientSet().CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		c.mu.Lock()
		_, done := c.collected[pod.Name]
		c.mu.Unlock()
		if done {
			continue
		}
		if docs := c.collectPod(ctx, pod); len(docs) > 0 {
			c.mu.Lock()
			c.collected[pod.Name] = docs
			c.mu.Unlock()
			log.Infof("Collected %d result documents from pod %s", len(docs), pod.Name)
		}
	}
}

// collectPod reads the pod's results directory and converts what it finds.
func (c *enmaasCollector) collectPod(ctx context.Context, pod corev1.Pod) []any {
	files, err := podfiles.Scan(ctx, getK8SConnector().RestConfig(),
		podfiles.Target{Namespace: c.namespace, Pod: pod.Name, Container: enmaasContainer},
		enmaasResultsDir, "*.json")
	if err != nil || len(files) == 0 {
		return nil
	}

	image, imageID := enmaasLoadgenImage(pod)
	target := pod.Labels[enmaasTargetLabel]
	docs, err := kbmetrics.ResultDocuments(files, kbmetrics.ResultsConfig{
		UUID:       c.uuid,
		MetricName: enmaasLevelMetric,
		JobName:    pod.Labels[enmaasJobLabel],
		Metadata:   c.metadata,
		Extra: map[string]any{
			"target":          target,
			"model":           pod.Labels[enmaasModelLabel],
			"concurrency":     pod.Labels[enmaasConcurrencyLabel],
			"url":             c.cfg.URLs[target],
			"durationSeconds": c.cfg.DurationSeconds,
			"maxTokens":       c.cfg.MaxTokens,
			"scriptName":      c.cfg.ScriptName,
			"podName":         pod.Name,
			"nodeName":        pod.Spec.NodeName,
			"loadtestImage":   image,
			"loadtestImageID": imageID,
		},
	})
	if err != nil {
		log.Errorf("Converting results from pod %s: %v", pod.Name, err)
		return nil
	}
	return docs
}

// documents returns everything collected so far.
func (c *enmaasCollector) documents() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var all []any
	for _, docs := range c.collected {
		all = append(all, docs...)
	}
	return all
}

// enmaasLoadgenImage reports the image the load generator ran and the digest
// it resolved to
func enmaasLoadgenImage(pod corev1.Pod) (string, string) {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == enmaasContainer {
			return status.Image, status.ImageID
		}
	}
	return "", ""
}

// indexEnmaasResults ships the harvested documents through every configured
// metrics endpoint, grouped by metricName so each lands under its own index.
func indexEnmaasResults(docs []any) {
	if len(docs) == 0 {
		return
	}
	byMetric := make(map[string][]any)
	for _, doc := range docs {
		metricName, _ := doc.(map[string]any)["metricName"].(string)
		if metricName == "" {
			metricName = enmaasLevelMetric
		}
		byMetric[metricName] = append(byMetric[metricName], doc)
	}

	indexed := false
	for _, endpoint := range workloads.ConfigSpec.MetricsEndpoints {
		if endpoint.Type == "" {
			continue
		}
		idx, err := indexers.NewIndexer(endpoint.IndexerConfig)
		if err != nil {
			log.Errorf("Failed to create indexer: %v", err)
			continue
		}
		for metricName, group := range byMetric {
			resp, err := (*idx).Index(group, indexers.IndexingOpts{MetricName: metricName})
			if err != nil {
				log.Errorf("Failed to index %s: %v", metricName, err)
				continue
			}
			log.Infof("Indexed %d %s documents: %s", len(group), metricName, resp)
			indexed = true
		}
	}
	if !indexed {
		log.Warn("No indexer was configured, enmaas-loadtest results were not persisted. " +
			"Pass --es-server/--es-index or enable --local-indexing.")
	}
}

// cleanupEnmaasNamespace removes all EnMaaS-related resources for a given run, identified by the UUID.
func cleanupEnmaasResources(ctx context.Context, namespace, uuid string) {
	k8s := getK8SConnector()
	selector := fmt.Sprintf("%s=%s", enmaasKubeBurnerUUIDLabel, uuid)
	propagation := metav1.DeletePropagationBackground
	options := metav1.DeleteOptions{PropagationPolicy: &propagation}
	listOptions := metav1.ListOptions{LabelSelector: selector}

	if err := k8s.ClientSet().BatchV1().Jobs(namespace).
		DeleteCollection(ctx, options, listOptions); err != nil {
		log.Warnf("Failed to delete Jobs in %s: %v", namespace, err)
	}
	podSelector := fmt.Sprintf("%s,%s=%s", enmaasAppLabel, enmaasUUIDLabel, uuid)
	if err := k8s.ClientSet().CoreV1().Pods(namespace).
		DeleteCollection(ctx, options, metav1.ListOptions{LabelSelector: podSelector}); err != nil {
		log.Warnf("Failed to delete pods in %s: %v", namespace, err)
	}
	if err := k8s.ClientSet().CoreV1().Secrets(namespace).
		DeleteCollection(ctx, options, listOptions); err != nil {
		log.Warnf("Failed to delete Secrets in %s: %v", namespace, err)
	}

	timeout, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	err := wait.PollUntilContextCancel(timeout, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		jobs, err := k8s.ClientSet().BatchV1().Jobs(namespace).List(ctx, listOptions)
		if err != nil {
			return false, nil
		}
		if len(jobs.Items) > 0 {
			log.Debugf("Waiting for %d Jobs to be deleted from %s", len(jobs.Items), namespace)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		log.Warnf("Timed out waiting for this run's resources to be removed from %s", namespace)
		return
	}
	log.Infof("Removed this run's resources from %s (namespace kept)", namespace)
}

// NewEnmaasLoadtest holds the EnMaaS gateway load test workload.
func NewEnmaasLoadtest(wh *workloads.WorkloadHelper) *cobra.Command {
	var gatewayURL, baselineURL, gatewayIP string
	var models, levels, maxKey string
	var duration time.Duration
	var tokens int
	var ttft bool
	var apiKey, apiKeyFile, apiKeySecret, apiKeySecretKey string
	var mintKey, revokeKey, podMint bool
	var serviceAccount string
	var meteringURL, meteringNamespace, meteringTokenSecret, meteringTokenSecretKey string
	var scriptName string
	var loadtestImage, imagePullPolicy, namespace string
	var scrapeGrace time.Duration
	var samples, parallelism int
	var pause, jobDeadline time.Duration
	var cpuRequest, cpuLimit, memoryRequest, memoryLimit string
	var cleanup bool
	var metricsProfiles []string
	var rc int

	cmd := &cobra.Command{
		Use:   "enmaas-loadtest",
		Short: "Runs enmaas-loadtest workload",
		Long: "Drives scripts/loadtest.py from redhat-et/enmaas-loadtest against the EnMaaS gateway\n" +
			"for each configured model, and indexes the per-concurrency-level latency, throughput\n" +
			"and error results into Elasticsearch alongside the usual kube-burner metrics.",
		Run: func(cmd *cobra.Command, args []string) {
			setMetrics(cmd, metricsProfiles)

			gatewayHostname := validateEnmaasURLs(gatewayURL, baselineURL)
			modelList := parseEnmaasModels(models)
			levelCount, err := parseEnmaasLevels(levels)
			if err != nil {
				log.Fatalf("Invalid --levels %q: %v", levels, err)
			}

			if meteringURL == "" {
				meteringURL = fmt.Sprintf("http://metering-service.%s.svc.cluster.local:8080", meteringNamespace)
			}

			var minted *enmaasMintedKey
			var metering *enmaasMeteringClient
			if mintKey {
				metering, minted = mintEnmaasRunKey(cmd.Context(), meteringURL, gatewayURL,
					meteringNamespace, meteringTokenSecret, meteringTokenSecretKey)
				apiKey = minted.Key
			}

			if podMint {
				if apiKey != "" || apiKeyFile != "" || apiKeySecret != "" || mintKey {
					log.Fatal("--pod-mint supplies the key from inside each pod and cannot be combined " +
						"with --mint-key, --api-key, --api-key-file or --api-key-secret")
				}
				if serviceAccount == "" {
					log.Fatal("--pod-mint requires --service-account: the pod authenticates to the " +
						"Kubernetes API with that ServiceAccount's token to read the metering secret")
				}
				AdditionalVars["API_KEY_B64"] = ""
			} else {
				AdditionalVars["API_KEY_B64"] = resolveEnmaasCredential(
					apiKey, apiKeyFile, apiKeySecret, apiKeySecretKey, "api-key", "API key", true)
			}

			if jobDeadline <= 0 {
				jobDeadline = enmaasJobDeadline(levelCount, samples, parallelism, duration)
			}
			log.Infof("Per-Job deadline (activeDeadlineSeconds): %s", jobDeadline)

			ttftFlag := ""
			if ttft {
				ttftFlag = "--ttft"
			}

			AdditionalVars["NAMESPACE"] = namespace
			AdditionalVars["URL"] = gatewayURL
			AdditionalVars["BASELINE_URL"] = baselineURL
			AdditionalVars["GATEWAY_IP"] = gatewayIP
			AdditionalVars["GATEWAY_HOSTNAME"] = gatewayHostname
			AdditionalVars["MODELS"] = strings.Join(modelList, ",")
			AdditionalVars["LEVELS"] = levels
			AdditionalVars["DURATION"] = int(duration.Seconds())
			AdditionalVars["TOKENS"] = tokens
			AdditionalVars["MAX_KEY"] = maxKey
			AdditionalVars["TTFT_FLAG"] = ttftFlag
			AdditionalVars["LOADTEST_IMAGE"] = loadtestImage
			AdditionalVars["IMAGE_PULL_POLICY"] = imagePullPolicy
			AdditionalVars["SCRIPT_NAME"] = scriptName
			AdditionalVars["SCRAPE_GRACE"] = int(scrapeGrace.Seconds())
			AdditionalVars["MINT"] = strconv.FormatBool(podMint)
			AdditionalVars["METERING_URL"] = meteringURL
			AdditionalVars["MINT_NAMESPACE"] = meteringNamespace
			AdditionalVars["MINT_SECRET"] = meteringTokenSecret
			AdditionalVars["MINT_SECRET_KEY"] = meteringTokenSecretKey
			AdditionalVars["SERVICE_ACCOUNT"] = serviceAccount
			AdditionalVars["SAMPLES"] = samples
			AdditionalVars["PARALLELISM"] = parallelism
			AdditionalVars["JOB_DEADLINE"] = int(jobDeadline.Seconds())
			AdditionalVars["PAUSE"] = pause
			AdditionalVars["CPU_REQUEST"] = cpuRequest
			AdditionalVars["CPU_LIMIT"] = cpuLimit
			AdditionalVars["MEMORY_REQUEST"] = memoryRequest
			AdditionalVars["MEMORY_LIMIT"] = memoryLimit
			AdditionalVars["GC"] = false

			uuid, _ := AdditionalVars["UUID"].(string)
			runConfig := enmaasRunConfig{
				URLs:            map[string]string{"gateway": gatewayURL, "baseline": baselineURL},
				DurationSeconds: int(duration.Seconds()),
				MaxTokens:       tokens,
				MaxKey:          maxKey,
				TTFT:            ttft,
				ScriptName:      scriptName,
			}

			collector := newEnmaasCollector(namespace, uuid, runConfig, wh.MetricsMetadata)
			watchCtx, stopWatch := context.WithCancel(context.Background())
			var watchDone sync.WaitGroup
			watchDone.Add(1)
			go func() {
				defer watchDone.Done()
				collector.watch(watchCtx, enmaasSweepInterval)
			}()

			rc = RunWorkload(cmd, wh, "enmaas-loadtest.yml")

			stopWatch()
			watchDone.Wait()

			docs := collector.documents()
			if len(docs) == 0 {
				log.Error("enmaas-loadtest produced no results to index")
				if rc == 0 {
					rc = 1
				}
			} else {
				indexEnmaasResults(docs)
			}

			if minted != nil && revokeKey {
				revokeCtx, cancelRevoke := context.WithTimeout(context.Background(), enmaasMintHTTPWait)
				if err := metering.revokeUser(revokeCtx, minted.Username); err != nil {
					log.Errorf("Failed to revoke throwaway user %s: %v. Revoke it manually with "+
						"DELETE %s%s/%s", minted.Username, err, meteringURL, enmaasMintUsersPath, minted.Username)
				} else {
					log.Infof("Revoked throwaway user %s", minted.Username)
				}
				cancelRevoke()
			} else if minted != nil {
				log.Warnf("Leaving throwaway user %s active (--revoke-key=false)", minted.Username)
			}

			if cleanup {
				cleanupEnmaasResources(context.Background(), namespace, uuid)
			} else {
				log.Infof("Leaving this run's resources in %s (--cleanup=false)", namespace)
			}
		},
		PostRun: func(cmd *cobra.Command, args []string) {
			os.Exit(rc)
		},
	}

	cmd.Flags().StringVar(&gatewayURL, "url", "", "Full gateway completions URL (e.g. https://api.enmaas.devshift.net/v1/chat/completions)")
	cmd.Flags().StringVar(&baselineURL, "baseline-url", "", "Optional second URL benchmarked identically, e.g. the simulator Service, to measure gateway overhead")
	cmd.Flags().StringVar(&gatewayIP, "gateway-ip", "", "Gateway ClusterIP for in-cluster DNS override via hostAliases")
	cmd.Flags().StringVar(&models, "models", "gpt-4o-openai,gpt-4o-azure,gpt-4o-bedrock,claude-sonnet-anthropic,claude-sonnet-vertex", "Comma separated model names, benchmarked one after another")
	cmd.Flags().StringVar(&levels, "levels", "100,200", "Comma separated concurrency levels passed to loadtest.py --levels")
	cmd.Flags().DurationVar(&duration, "duration", 300*time.Second, "Duration of each concurrency level")
	cmd.Flags().IntVar(&tokens, "tokens", 16, "Max tokens requested per completion")
	cmd.Flags().StringVar(&maxKey, "max-key", "max_tokens", "Request field carrying the token budget (max_tokens, max_completion_tokens, ...)")
	cmd.Flags().BoolVar(&ttft, "ttft", false, "Send stream:true and measure time to first token")
	cmd.Flags().BoolVar(&podMint, "pod-mint", false, "Let each Job pod mint and revoke its own API key, instead of sharing one minted by the wrapper")
	cmd.Flags().StringVar(&serviceAccount, "service-account", "", "ServiceAccount for the load generator pods; required with --pod-mint")
	cmd.Flags().BoolVar(&mintKey, "mint-key", false, "Mint a throwaway API key via the metering API before the run and reuse it for every model")
	cmd.Flags().BoolVar(&revokeKey, "revoke-key", true, "Revoke the minted user once the run finishes (only with --mint-key)")
	cmd.Flags().StringVar(&meteringURL, "metering-url", "", "Metering user-management API base URL; defaults to the in-cluster metering-service, use a port-forward from outside")
	cmd.Flags().StringVar(&meteringNamespace, "metering-namespace", "enmaas", "Namespace holding the metering service and its API token Secret")
	cmd.Flags().StringVar(&meteringTokenSecret, "metering-token-secret", "metering-user-management-api", "Secret holding the metering user-management API token")
	cmd.Flags().StringVar(&meteringTokenSecretKey, "metering-token-secret-key", "token", "Key within --metering-token-secret that holds the token")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key used for every request, given literally; the workload deploys it as a Secret in the test namespace")
	cmd.Flags().StringVar(&apiKeyFile, "api-key-file", "", "Local file holding the API key used for every request (mutually exclusive with --api-key-secret)")
	cmd.Flags().StringVar(&apiKeySecret, "api-key-secret", "", "Existing Secret holding the API key, as <namespace>/<name> (mutually exclusive with --api-key-file)")
	cmd.Flags().StringVar(&apiKeySecretKey, "api-key-secret-key", enmaasKeySecretEntry, "Key within --api-key-secret that holds the API key")
	cmd.Flags().StringVar(&scriptName, "script-name", enmaasDefaultScript, "Harness in the repo's scripts/ directory to run")
	cmd.Flags().DurationVar(&scrapeGrace, "scrape-grace", 45*time.Second, "How long each pod stays alive after the harness finishes, so its results can be collected")
	cmd.Flags().StringVar(&loadtestImage, "loadtest-image", enmaasDefaultImage, "Load generator image built from the enmaas-loadtest repository")
	cmd.Flags().StringVar(&imagePullPolicy, "image-pull-policy", "Always", "imagePullPolicy for the load generator image")
	cmd.Flags().StringVar(&namespace, "namespace", "enmaas-stage-tests", "Namespace the load generator Jobs run in; it is never deleted")
	cmd.Flags().IntVar(&samples, "samples", 1, "Number of repeats per model (Job completions)")
	cmd.Flags().IntVar(&parallelism, "parallelism", 1, "Job parallelism per model")
	cmd.Flags().DurationVar(&pause, "pause", 10*time.Second, "Pause after each model's Job completes")
	cmd.Flags().DurationVar(&jobDeadline, "job-deadline", 0, "Hard per-Job deadline (activeDeadlineSeconds); 0 derives it from levels, samples and duration")
	cmd.Flags().StringVar(&cpuRequest, "cpu-request", "1", "CPU request for the load generator pod")
	cmd.Flags().StringVar(&cpuLimit, "cpu-limit", "4", "CPU limit for the load generator pod")
	cmd.Flags().StringVar(&memoryRequest, "memory-request", "1Gi", "Memory request for the load generator pod")
	cmd.Flags().StringVar(&memoryLimit, "memory-limit", "2Gi", "Memory limit for the load generator pod")
	cmd.Flags().BoolVar(&cleanup, "cleanup", true, "Remove this run's Jobs, pods and Secrets once results are collected; the namespace itself is kept")
	cmd.Flags().StringSliceVar(&metricsProfiles, "metrics-profile", []string{"enmaas-loadtest-metrics.yml"}, "Comma separated list of metrics profiles to use")
	cmd.MarkFlagRequired("url")
	cmd.MarkFlagsMutuallyExclusive("mint-key", "api-key", "api-key-file", "api-key-secret")
	return cmd
}
