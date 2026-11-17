#!/usr/bin/env bash
#
# Makes the agentic operator's controller-runtime metrics reachable from
# Prometheus so the controller-health half of agentic-metrics.yml returns data.
#
# Three things are missing on a stock cluster and all three are required:
#
#   1. User workload monitoring is off by default, so nothing scrapes anything
#      in openshift-lightspeed, not even the mock LLM's ServiceMonitor.
#   2. The operator exposes a `metrics` container port on 8080 but has no
#      Service in front of it, and a ServiceMonitor selects Services.
#   3. The operator ships no ServiceMonitor of its own.
#
# This script does 2 and 3. It deliberately does not do 1: enabling user
# workload monitoring edits cluster-monitoring-config in openshift-monitoring,
# which is cluster-wide state that other teams may share, so it stays a
# conscious cluster-admin step. The script checks the prerequisite and prints
# the command rather than running it.
#
# Without all three, reconcileDurationP99, reconcileRate, reconcileErrors,
# workqueue* and goGoroutines all index as empty, which silently removes the
# OLS-3066 reconcile SLO from the run. Run this before agentic-run-density.
#
# The endpoint is unauthenticated HTTP: cmd/main.go binds
# --metrics-bind-address to ":8080" and installs no authn/authz filter, so the
# ServiceMonitor needs no bearer token or TLS config. If the operator ever
# moves to the kubebuilder default of a secured :8443, this script and
# agentic-metrics.yml both have to change.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSET_DIR="${SCRIPT_DIR}/agentic-metrics"

NAMESPACE="openshift-lightspeed"
INTERVAL="15s"
DRY_RUN="false"
DELETE="false"

usage() {
  cat <<EOF
Usage: $(basename "$0") [options]

Creates the Service and ServiceMonitor that expose the agentic operator's
controller-runtime metrics to Prometheus, and verifies the target is scraped.

Requires user workload monitoring to be enabled already. To enable it:

  oc -n openshift-monitoring patch configmap cluster-monitoring-config \\
    --type merge -p '{"data":{"config.yaml":"enableUserWorkload: true\\n"}}'

That command replaces config.yaml wholesale. If the ConfigMap already has
other settings, edit it by hand instead and add enableUserWorkload: true.

Options:
  --namespace NS     Agentic operator namespace (default: ${NAMESPACE})
  --interval DUR     Scrape interval (default: ${INTERVAL})
  --dry-run          Print the manifests and exit
  --delete           Remove the Service and ServiceMonitor
  -h, --help         Show this help

Example:
  ./enable-agentic-metrics.sh
  ./enable-agentic-metrics.sh --interval=30s
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --namespace=*|--interval=*)
      set -- "${1%%=*}" "${1#*=}" "${@:2}" ;;
  esac
  case "$1" in
    --namespace) NAMESPACE="$2"; shift 2 ;;
    --interval) INTERVAL="$2"; shift 2 ;;
    --dry-run) DRY_RUN="true"; shift ;;
    --delete) DELETE="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 1 ;;
  esac
done

log() { echo "[agentic-metrics] $*"; }
die() { echo "[agentic-metrics] ERROR: $*" >&2; exit 1; }

render() {
  sed -e "s|__NAMESPACE__|${NAMESPACE}|g" \
      -e "s|__INTERVAL__|${INTERVAL}|g" \
      "$1"
}

command -v oc >/dev/null 2>&1 || die "oc is not on PATH"
[[ -f "${ASSET_DIR}/manifests.yml" ]] || die "missing ${ASSET_DIR}/manifests.yml"

if [[ "${DRY_RUN}" == "true" ]]; then
  render "${ASSET_DIR}/manifests.yml"
  exit 0
fi

oc whoami >/dev/null 2>&1 || die "not logged in to a cluster"

if [[ "${DELETE}" == "true" ]]; then
  log "removing the metrics Service and ServiceMonitor from ${NAMESPACE}"
  oc delete -n "${NAMESPACE}" servicemonitor lightspeed-agentic-operator --ignore-not-found 2>/dev/null || true
  oc delete -n "${NAMESPACE}" service lightspeed-agentic-operator-metrics --ignore-not-found
  log "done"
  exit 0
fi

oc get namespace "${NAMESPACE}" >/dev/null 2>&1 \
  || die "namespace ${NAMESPACE} does not exist; point --namespace at the agentic operator's namespace"

oc get deployment lightspeed-agentic-operator -n "${NAMESPACE}" >/dev/null 2>&1 \
  || die "deployment lightspeed-agentic-operator not found in ${NAMESPACE}; is the agentic operator installed?"

# The Service targets the port by name, so a rename upstream has to fail loudly
# here rather than produce a Service with no endpoints.
oc get deployment lightspeed-agentic-operator -n "${NAMESPACE}" \
  -o jsonpath='{.spec.template.spec.containers[*].ports[*].name}' | grep -qw metrics \
  || die "the operator container has no port named 'metrics'; check --metrics-bind-address and update ${ASSET_DIR}/manifests.yml"

oc get crd servicemonitors.monitoring.coreos.com >/dev/null 2>&1 \
  || die "servicemonitors.monitoring.coreos.com not found; the monitoring stack is not installed"

if ! oc get statefulset prometheus-user-workload -n openshift-user-workload-monitoring >/dev/null 2>&1; then
  die "user workload monitoring is not enabled, so nothing will scrape the ServiceMonitor.
Enable it first (cluster-wide change, see --help), then re-run:

  oc -n openshift-monitoring patch configmap cluster-monitoring-config \\
    --type merge -p '{\"data\":{\"config.yaml\":\"enableUserWorkload: true\\n\"}}'

If cluster-monitoring-config already holds other settings, edit it by hand and
add enableUserWorkload: true rather than replacing config.yaml."
fi

log "applying the metrics Service and ServiceMonitor in ${NAMESPACE}"
render "${ASSET_DIR}/manifests.yml" | oc apply -f -

# A Service with no endpoints is the failure this whole script exists to
# prevent, and it is invisible until a metrics query comes back empty an hour
# into a soak.
log "checking the Service has endpoints"
eps=""
for _ in $(seq 1 30); do
  eps="$(oc get endpoints lightspeed-agentic-operator-metrics -n "${NAMESPACE}" \
    -o jsonpath='{.subsets[*].addresses[*].ip}' 2>/dev/null || true)"
  [[ -n "${eps}" ]] && break
  sleep 2
done
[[ -n "${eps}" ]] || die "Service lightspeed-agentic-operator-metrics has no endpoints; the pod selector does not match"
log "endpoints: ${eps}"

log "waiting for the first successful scrape"
host="$(oc get route thanos-querier -n openshift-monitoring -o jsonpath='{.spec.host}' 2>/dev/null || true)"
if [[ -z "${host}" ]]; then
  log "WARNING: thanos-querier route not found, skipping the scrape check"
  log "done"
  exit 0
fi

token="$(oc whoami -t)"
result=""
for _ in $(seq 1 40); do
  # An empty result and a result of 0 are different failures: no series at all
  # means the target is not registered, a 0 means it is registered and the
  # scrape itself is failing.
  result="$(curl -sk -H "Authorization: Bearer ${token}" \
    --data-urlencode 'query=up{job="lightspeed-agentic-operator-metrics"}' \
    "https://${host}/api/v1/query" 2>/dev/null \
    | python3 -c 'import json,sys; r=json.load(sys.stdin)["data"]["result"]; print(r[0]["value"][1] if r else "")' 2>/dev/null || true)"
  [[ "${result}" == "1" ]] && break
  sleep 10
done

[[ "${result}" == "1" ]] \
  || die "target never reported up (last value: '${result:-none}'); check 'oc get servicemonitor -n ${NAMESPACE}' and the user workload Prometheus targets page"

log "target is up and being scraped"
log "done. agentic-metrics.yml controller-health queries should now return data."
