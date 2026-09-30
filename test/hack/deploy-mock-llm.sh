#!/bin/bash
# Copyright 2026 The Kube-burner Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Stand up the mock LLM that agentic-run-density runs against, and wire the
# agentic CRD chain to it.
#
# Running agentic-run-density against a real model measures the model, not the
# operator: inference latency dominates and varies run to run, so nothing is
# comparable. This script replaces the model with a deterministic, scriptable
# one and leaves the whole operator and sandbox path intact.
#
# It creates, in order:
#
#   ConfigMap       the mock server source, so there is no image to build
#   Secret          OPENAI_API_KEY, which the operator mounts unconditionally
#   Deployment      stock UBI python running the mock
#   Service         mock-llm:8080
#   ServiceMonitor  optional, needs user workload monitoring
#   LLMProvider     type OpenAI, pointed at the Service
#   Agent           binds the provider to a model name and the turn budget
#   ApprovalPolicy  the cluster singleton; without it every run stalls
#
# Idempotent: re-running applies over the top. Use --delete to remove it all.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSET_DIR="${SCRIPT_DIR}/mock-llm"

NAMESPACE="openshift-lightspeed"
REPLICAS=2
IMAGE="registry.access.redhat.com/ubi9/python-311:latest"
DEFAULT_PROFILE="typical"
SLEEP_SECONDS="0"
MODEL="mock-model"
AGENT_NAME="default"
PROVIDER_NAME="mock-llm"
SECRET_NAME="mock-llm-credentials"
CONFIGMAP_NAME="mock-llm-server"
MAX_TURNS=200
MAX_CONCURRENT_RUNS=20
WITH_SERVICEMONITOR="false"
DRY_RUN="false"
DELETE="false"
TIMEOUT="300s"

usage() {
  cat <<'EOF'
Usage: deploy-mock-llm.sh [options]

  --namespace NAME            Operator namespace to deploy into (default: openshift-lightspeed).
                              Must be the namespace the agentic operator runs in: the
                              credentials Secret is resolved there and nowhere else.
  --replicas N                Mock server replicas (default: 2). Raise for high concurrency;
                              a saturated mock shows up as operator latency.
  --image REF                 Container image to run the mock in (default: UBI 9 python 3.11).
                              Any image with python3 on PATH works.
  --default-profile NAME      Profile used when a request carries no [mock-profile:...] token
                              (default: typical). One of trivial, short, typical, long,
                              max-turns, timeout, malformed.
  --sleep SECONDS             Extra synthetic latency per response (default: 0). Zero-latency
                              inference hides queueing; 2-8 is realistic.
  --model NAME                Model string on the Agent CR (default: mock-model).
  --agent-name NAME           Agent CR to create (default: default). The operator uses the
                              Agent named "default" whenever a run step omits spec.<step>.agent.
  --provider-name NAME        LLMProvider CR name (default: mock-llm).
  --secret-name NAME          Credentials Secret name (default: mock-llm-credentials).
  --max-turns N               Agent.spec.maxTurns, 1-500 (default: 200).
  --max-concurrent-runs N     ApprovalPolicy.spec.maxConcurrentRuns, 1-20 (default: 20).
                              This is the operator's concurrency ceiling. The product default
                              is 5; leaving it there caps throughput no matter how many runs
                              the workload creates.
  --with-servicemonitor       Also create a ServiceMonitor for the mock's own counters.
                              Requires user workload monitoring to be enabled.
  --dry-run                   Render everything to stdout and apply nothing.
  --delete                    Remove everything this script creates.
  -h, --help                  Show this help.

Examples:
  # Default: deterministic, zero added latency, concurrency ceiling raised
  ./deploy-mock-llm.sh

  # Realistic inference latency and a bigger mock for a concurrency test
  ./deploy-mock-llm.sh --sleep 4 --replicas 6 --with-servicemonitor

  # Tear down
  ./deploy-mock-llm.sh --delete
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --namespace=*|--replicas=*|--image=*|--default-profile=*|--sleep=*|--model=*|\
    --agent-name=*|--provider-name=*|--secret-name=*|--max-turns=*|--max-concurrent-runs=*)
      set -- "${1%%=*}" "${1#*=}" "${@:2}" ;;
  esac
  case "$1" in
    --namespace) NAMESPACE="$2"; shift 2 ;;
    --replicas) REPLICAS="$2"; shift 2 ;;
    --image) IMAGE="$2"; shift 2 ;;
    --default-profile) DEFAULT_PROFILE="$2"; shift 2 ;;
    --sleep) SLEEP_SECONDS="$2"; shift 2 ;;
    --model) MODEL="$2"; shift 2 ;;
    --agent-name) AGENT_NAME="$2"; shift 2 ;;
    --provider-name) PROVIDER_NAME="$2"; shift 2 ;;
    --secret-name) SECRET_NAME="$2"; shift 2 ;;
    --max-turns) MAX_TURNS="$2"; shift 2 ;;
    --max-concurrent-runs) MAX_CONCURRENT_RUNS="$2"; shift 2 ;;
    --with-servicemonitor) WITH_SERVICEMONITOR="true"; shift ;;
    --dry-run) DRY_RUN="true"; shift ;;
    --delete) DELETE="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 1 ;;
  esac
done

log() { echo "[mock-llm] $*"; }
die() { echo "[mock-llm] ERROR: $*" >&2; exit 1; }

render() {
  sed -e "s|__NAMESPACE__|${NAMESPACE}|g" \
      -e "s|__REPLICAS__|${REPLICAS}|g" \
      -e "s|__IMAGE__|${IMAGE}|g" \
      -e "s|__DEFAULT_PROFILE__|${DEFAULT_PROFILE}|g" \
      -e "s|__SLEEP_SECONDS__|${SLEEP_SECONDS}|g" \
      -e "s|__MODEL__|${MODEL}|g" \
      -e "s|__AGENT_NAME__|${AGENT_NAME}|g" \
      -e "s|__PROVIDER_NAME__|${PROVIDER_NAME}|g" \
      -e "s|__SECRET_NAME__|${SECRET_NAME}|g" \
      -e "s|__CONFIGMAP_NAME__|${CONFIGMAP_NAME}|g" \
      -e "s|__MAX_TURNS__|${MAX_TURNS}|g" \
      -e "s|__MAX_CONCURRENT_RUNS__|${MAX_CONCURRENT_RUNS}|g" \
      "$1"
}

command -v oc >/dev/null 2>&1 || die "oc is not on PATH"
[[ -f "${ASSET_DIR}/mock_openai_server.py" ]] || die "missing ${ASSET_DIR}/mock_openai_server.py"

if [[ "${DELETE}" == "true" ]]; then
  log "removing the mock LLM and its CRD chain from ${NAMESPACE}"
  oc delete agent.agentic.openshift.io "${AGENT_NAME}" --ignore-not-found
  oc delete llmprovider.agentic.openshift.io "${PROVIDER_NAME}" --ignore-not-found
  oc delete approvalpolicy.agentic.openshift.io cluster --ignore-not-found
  oc delete -n "${NAMESPACE}" servicemonitor mock-llm --ignore-not-found 2>/dev/null || true
  oc delete -n "${NAMESPACE}" service mock-llm --ignore-not-found
  oc delete -n "${NAMESPACE}" deployment mock-llm --ignore-not-found
  oc delete -n "${NAMESPACE}" secret "${SECRET_NAME}" --ignore-not-found
  oc delete -n "${NAMESPACE}" configmap "${CONFIGMAP_NAME}" --ignore-not-found
  log "done"
  exit 0
fi

if [[ "${DRY_RUN}" == "true" ]]; then
  oc create configmap "${CONFIGMAP_NAME}" \
    --from-file="mock_openai_server.py=${ASSET_DIR}/mock_openai_server.py" \
    --namespace "${NAMESPACE}" --dry-run=client -o yaml
  echo "---"
  render "${ASSET_DIR}/manifests.yml"
  if [[ "${WITH_SERVICEMONITOR}" == "true" ]]; then
    echo "---"
    render "${ASSET_DIR}/servicemonitor.yml"
  fi
  echo "---"
  render "${ASSET_DIR}/agentic-config.yml"
  exit 0
fi

oc whoami >/dev/null 2>&1 || die "not logged in to a cluster"

for crd in llmproviders agents approvalpolicies agenticruns; do
  oc get crd "${crd}.agentic.openshift.io" >/dev/null 2>&1 \
    || die "CRD ${crd}.agentic.openshift.io not found; is lightspeed-agentic-operator installed?"
done

oc get namespace "${NAMESPACE}" >/dev/null 2>&1 \
  || die "namespace ${NAMESPACE} does not exist; point --namespace at the agentic operator's namespace"

log "deploying into ${NAMESPACE}: ${REPLICAS} replica(s), default profile ${DEFAULT_PROFILE}, +${SLEEP_SECONDS}s per response"

log "creating ConfigMap ${CONFIGMAP_NAME} from mock_openai_server.py"
oc create configmap "${CONFIGMAP_NAME}" \
  --from-file="mock_openai_server.py=${ASSET_DIR}/mock_openai_server.py" \
  --namespace "${NAMESPACE}" --dry-run=client -o yaml | oc apply -f -

log "applying Secret, Deployment and Service"
render "${ASSET_DIR}/manifests.yml" | oc apply -f -

if oc get -n "${NAMESPACE}" deployment mock-llm -o jsonpath='{.status.observedGeneration}' >/dev/null 2>&1; then
  oc rollout restart -n "${NAMESPACE}" deployment/mock-llm >/dev/null
fi

if [[ "${WITH_SERVICEMONITOR}" == "true" ]]; then
  if oc get crd servicemonitors.monitoring.coreos.com >/dev/null 2>&1; then
    log "applying ServiceMonitor"
    render "${ASSET_DIR}/servicemonitor.yml" | oc apply -f -
    # A ServiceMonitor with user workload monitoring off is inert: it applies
    # cleanly and scrapes nothing, so the mock*/saturation-guard metrics come
    # back empty hours later with no error to point at.
    oc get statefulset prometheus-user-workload -n openshift-user-workload-monitoring >/dev/null 2>&1 \
      || log "WARNING: user workload monitoring is not enabled, so this ServiceMonitor will not be scraped. See test/hack/enable-agentic-metrics.sh --help"
  else
    log "WARNING: servicemonitors.monitoring.coreos.com not found, skipping ServiceMonitor"
  fi
fi

log "waiting for the mock to become ready"
oc rollout status -n "${NAMESPACE}" deployment/mock-llm --timeout="${TIMEOUT}"

log "applying LLMProvider, Agent and ApprovalPolicy"
render "${ASSET_DIR}/agentic-config.yml" | oc apply -f -

log "smoke testing /v1/chat/completions"
POD="$(oc get pods -n "${NAMESPACE}" -l app.kubernetes.io/name=mock-llm \
  -o jsonpath='{.items[0].metadata.name}')"
oc exec -n "${NAMESPACE}" "${POD}" -- python3 -c '
import json, urllib.request
body = json.dumps({
    "model": "mock-model",
    "messages": [{"role": "user", "content": "smoke test [mock-profile:trivial]"}],
}).encode()
req = urllib.request.Request(
    "http://127.0.0.1:8080/v1/chat/completions",
    data=body, headers={"Content-Type": "application/json"})
with urllib.request.urlopen(req, timeout=30) as resp:
    payload = json.load(resp)
content = json.loads(payload["choices"][0]["message"]["content"])
assert content["actionRequired"] == "False", content
print("  terminal payload:", json.dumps(content)[:120] + "...")
print("  usage:", payload["usage"])
' || die "smoke test failed; check 'oc logs -n ${NAMESPACE} deployment/mock-llm'"

cat <<EOF

[mock-llm] ready.

  Service        http://mock-llm.${NAMESPACE}.svc.cluster.local:8080/v1
  LLMProvider    ${PROVIDER_NAME}  (type: OpenAI)
  Agent          ${AGENT_NAME}     (model: ${MODEL}, maxTurns: ${MAX_TURNS})
  ApprovalPolicy cluster           (maxConcurrentRuns: ${MAX_CONCURRENT_RUNS}, all stages Automatic)

Drive load against it:

  kube-burner-ocp agentic-run-density --iterations=100 \\
    --agent=${AGENT_NAME} --mock-profile=typical \\
    --target-namespace=perf-target --target-namespace-count=20

Read the mock's own counters:

  oc exec -n ${NAMESPACE} deploy/mock-llm -- python3 -c \\
    "import urllib.request;print(urllib.request.urlopen('http://127.0.0.1:8080/metrics').read().decode())"

EOF
