#!/bin/bash
# create-event-storm.sh — Creates synthetic Kubernetes events at high throughput
# Designed to run as a background hook alongside a victim workload.
#
# Usage: create-event-storm.sh <namespace> <iterations> <replicas> <parallel_workers>

set -euo pipefail

NAMESPACE="${1:?namespace required}"
TOTAL_ITERATIONS="${2:-2000}"
REPLICAS="${3:-100}"
PARALLEL="${4:-20}"
TOTAL_EVENTS=$((TOTAL_ITERATIONS * REPLICAS))

echo "Event storm: creating ${TOTAL_EVENTS} events (${TOTAL_ITERATIONS} iterations x ${REPLICAS} replicas) in namespace ${NAMESPACE} with ${PARALLEL} workers"

# Ensure namespace exists
oc create namespace "${NAMESPACE}" --dry-run=client -o yaml 2>/dev/null | oc apply -f - 2>/dev/null || true
oc label namespace "${NAMESPACE}" --overwrite \
  security.openshift.io/scc.podSecurityLabelSync=false \
  pod-security.kubernetes.io/enforce=privileged \
  pod-security.kubernetes.io/audit=privileged \
  kube-burner.io/job=victim-workload \
  pod-security.kubernetes.io/warn=privileged 2>/dev/null || true

EVENT_MESSAGE="synthetic event storm padded to approximately one kilobyte to match realistic event sizes from FailedScheduling and BackOff messages which carry full pod spec context including container images resource requests limits environment variables volume mounts service account tokens and scheduling constraints that inflate the event payload significantly beyond what a simple test message would produce in practice so we need to ensure our test events are representative of real world event sizes that flow through the Kubernetes event pipeline and into etcd storage where they contribute to database growth WAL pressure and compaction overhead during normal cluster operations under load"

create_batch() {
  local iter=$1
  local replicas=$2
  local ns=$3
  local msg=$4
  local yaml=""
  for ((r = 0; r < replicas; r++)); do
    yaml+="---
apiVersion: v1
kind: Event
metadata:
  namespace: ${ns}
  name: storm-${iter}-${r}
involvedObject:
  apiVersion: v1
  kind: Pod
  name: phantom-${iter}-${r}
  namespace: ${ns}
reason: EtcdLoadTest
type: Warning
message: \"${msg}\"
source:
  component: kube-burner
count: 1
"
  done
  echo "${yaml}" | oc create -f - 2>/dev/null || true
}

export -f create_batch
export NAMESPACE EVENT_MESSAGE

seq 0 $((TOTAL_ITERATIONS - 1)) | xargs -P "${PARALLEL}" -I {} \
  bash -c "create_batch {} ${REPLICAS} ${NAMESPACE} \"\${EVENT_MESSAGE}\""

echo "Event storm complete: ${TOTAL_EVENTS} events created in namespace ${NAMESPACE}"
