#!/usr/bin/env bash
#
# beforeCleanup hook for cudn-density (--ssh-latency-check). Waits for
# RouteAdvertisements to be accepted, then SSHes into each CUDN server pod
# via its CUDN IP and writes one NDJSON result per pod for the sshCheck
# measurement to index.
#
# Usage: cudn-ssh-test.sh <uuid> <ssh-private-key-path> [job-name] [parallelism]

set -uo pipefail

UUID="${1:?usage: cudn-ssh-test.sh <uuid> <ssh-private-key-path> [job-name] [parallelism]}"
SSH_KEY="${2:?usage: cudn-ssh-test.sh <uuid> <ssh-private-key-path> [job-name] [parallelism]}"
JOB_NAME="${3:-cudn-density}"
PARALLELISM="${4:-30}"

RESULTS_FILE="/tmp/kube-burner-ssh-results-${UUID}.ndjson"
SSH_USER="sshtest"
SSH_PORT=2222

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

echo "cudn-ssh-test.sh: waiting for all RouteAdvertisements to be accepted..."
if ! oc wait routeadvertisements -l "kube-burner.io/job=${JOB_NAME}" \
    --for=jsonpath='{.status.conditions[0].type}'=Accepted --timeout=120s 2>/dev/null; then
    echo "cudn-ssh-test.sh: warning - oc wait for RA acceptance timed out or failed, proceeding anyway" >&2
fi

TOTAL_PEERS=$(podman exec frr vtysh -c "show bgp summary" 2>/dev/null | awk '/^[0-9]+\./ {count++} END {print count+0}')
echo "cudn-ssh-test.sh: waiting for BGP routes to propagate to all ${TOTAL_PEERS} peers..."
DEADLINE=$(($(date +%s) + 120))
while true; do
    PEERS_WITH_ROUTES=$(podman exec frr vtysh -c "show bgp summary" 2>/dev/null | awk '/^[0-9]+\./ {if ($(NF-2)+0 > 0) count++} END {print count+0}')
    if [ "$PEERS_WITH_ROUTES" -ge "$TOTAL_PEERS" ] && [ "$TOTAL_PEERS" -gt 0 ] 2>/dev/null; then
        echo "cudn-ssh-test.sh: all ${PEERS_WITH_ROUTES} peers have routes, waiting 15s for OVN-K datapath to settle..."
        sleep 15
        break
    fi
    if [ "$(date +%s)" -ge "$DEADLINE" ]; then
        echo "cudn-ssh-test.sh: WARNING - BGP routes not on all peers after 120s (${PEERS_WITH_ROUTES}/${TOTAL_PEERS} have routes), proceeding anyway" >&2
        break
    fi
    sleep 5
done

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

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
echo "cudn-ssh-test.sh: checking SSH connectivity to ${TOTAL} pod(s) via their CUDN IP (port=${SSH_PORT}, parallelism=${PARALLELISM})"

check_one() {
    local ns="$1" pod="$2" ip="$3"
    local start_ms end_ms latency_ms success

    start_ms=$(date +%s%3N)
    if ssh -n -p "$SSH_PORT" -o ConnectTimeout=10 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
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
export SSH_KEY SSH_USER SSH_PORT WORKDIR

xargs -P "$PARALLELISM" -n 3 bash -c 'check_one "$@"' -- < "${WORKDIR}/targets.txt"

find "${WORKDIR}" -maxdepth 1 -name '*.json' -exec cat {} + > "$RESULTS_FILE"

OK_COUNT=$(grep -c '"success":true' "$RESULTS_FILE" || true)
FAIL_COUNT=$(grep -c '"success":false' "$RESULTS_FILE" || true)
echo "cudn-ssh-test.sh: SSH check complete - ${OK_COUNT} succeeded, ${FAIL_COUNT} failed (results: ${RESULTS_FILE})"

if [ "$FAIL_COUNT" -gt 0 ]; then
    grep '"success":false' "$RESULTS_FILE" >&2
fi
