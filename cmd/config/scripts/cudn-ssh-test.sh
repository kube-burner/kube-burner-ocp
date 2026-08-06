#!/usr/bin/env bash
#
# beforeCleanup hook for cudn-density (--bgp only). SSHes into each CUDN
# server pod via its CUDN IP and writes one NDJSON result per pod for the
# sshCheck measurement to index. Adapted from
# https://gist.github.com/venkataanil/50cef5235f1f870982666b7ff81ac830
# (credit: @jtaleric), trimmed to a single pass with the OVS analysis removed.
#
# Usage: cudn-ssh-test.sh <uuid> <ssh-private-key-path> [job-name] [parallelism]

set -uo pipefail

UUID="${1:?usage: cudn-ssh-test.sh <uuid> <ssh-private-key-path> [job-name] [parallelism]}"
SSH_KEY="${2:?usage: cudn-ssh-test.sh <uuid> <ssh-private-key-path> [job-name] [parallelism]}"
JOB_NAME="${3:-cudn-density}"
PARALLELISM="${4:-30}"

RESULTS_FILE="/tmp/kube-burner-ssh-results-${UUID}.ndjson"
SSH_USER="sshtest"

for bin in oc jq ssh; do
    if ! command -v "$bin" >/dev/null 2>&1; then
        echo "cudn-ssh-test.sh: required binary '$bin' not found in PATH" >&2
        exit 1
    fi
done

if [ ! -r "$SSH_KEY" ]; then
    echo "cudn-ssh-test.sh: SSH private key not readable: $SSH_KEY" >&2
    exit 1
fi

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

# CUDN IP lives in the k8s.ovn.org/pod-networks annotation, keyed by the
# per-namespace UDN network name (prefixed by the namespace name); this
# skips the primary cluster network's entry, leaving only the CUDN address.
oc get pod -l "kube-burner.io/job=${JOB_NAME},app=nginx" -A -o json | jq -r '
  .items[] |
  .metadata.namespace as $ns | .metadata.name as $pod |
  (.metadata.annotations["k8s.ovn.org/pod-networks"] // "" |
   if . == "" then null
   else fromjson | to_entries[] | select(.key | startswith($ns)) | .value.ip_address // "" | split("/")[0]
   end) as $ip |
  select($ip != null and $ip != "") |
  "\($ns) \($pod) \($ip)"
' > "${WORKDIR}/targets.txt"

TOTAL=$(wc -l < "${WORKDIR}/targets.txt" | tr -d ' ')
if [ "$TOTAL" -eq 0 ]; then
    echo "cudn-ssh-test.sh: no nginx-ssh server pods with a CUDN IP found for job '${JOB_NAME}'" >&2
    : > "$RESULTS_FILE"
    exit 0
fi
echo "cudn-ssh-test.sh: checking SSH connectivity to ${TOTAL} pod(s) via their CUDN IP (parallelism=${PARALLELISM})"

check_one() {
    local ns="$1" pod="$2" ip="$3"
    local start_ms end_ms latency_ms success

    start_ms=$(date +%s%3N)
    # whoami (not hostname) - the nginx-ssh image is built on ubi-minimal,
    # which doesn't ship util-linux's hostname binary.
    if ssh -n -o ConnectTimeout=10 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o LogLevel=ERROR -i "$SSH_KEY" "${SSH_USER}@${ip}" whoami >/dev/null 2>&1; then
        success=true
    else
        success=false
    fi
    end_ms=$(date +%s%3N)
    latency_ms=$((end_ms - start_ms))

    printf '{"namespace":"%s","pod":"%s","ip":"%s","success":%s,"latencyMs":%d}\n' \
        "$ns" "$pod" "$ip" "$success" "$latency_ms" > "${WORKDIR}/${ns}__${pod}.json"
}
export -f check_one
export SSH_KEY SSH_USER WORKDIR

xargs -P "$PARALLELISM" -n 3 bash -c 'check_one "$@"' -- < "${WORKDIR}/targets.txt"

find "${WORKDIR}" -maxdepth 1 -name '*.json' -exec cat {} + > "$RESULTS_FILE"

OK_COUNT=$(grep -c '"success":true' "$RESULTS_FILE" || true)
FAIL_COUNT=$(grep -c '"success":false' "$RESULTS_FILE" || true)
echo "cudn-ssh-test.sh: SSH check complete - ${OK_COUNT} succeeded, ${FAIL_COUNT} failed (results: ${RESULTS_FILE})"

if [ "$FAIL_COUNT" -gt 0 ]; then
    grep '"success":false' "$RESULTS_FILE" >&2
fi
