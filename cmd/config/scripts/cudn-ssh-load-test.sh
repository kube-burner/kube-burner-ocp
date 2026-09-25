#!/usr/bin/env bash
#
# beforeCleanup hook for cudn-density (--ssh-load-test). After the latency
# check completes, this script opens parallel SSH connections to ALL server
# pods simultaneously and keeps reconnecting for the specified duration,
# reporting total successes and failures.
#
# Usage: cudn-ssh-load-test.sh <uuid> <ssh-private-key-path> <duration-seconds> [job-name]

set -uo pipefail

UUID="${1:?usage: cudn-ssh-load-test.sh <uuid> <ssh-private-key-path> <duration-seconds> [job-name]}"
SSH_KEY="${2:?usage: cudn-ssh-load-test.sh <uuid> <ssh-private-key-path> <duration-seconds> [job-name]}"
DURATION_SECS="${3:?usage: cudn-ssh-load-test.sh <uuid> <ssh-private-key-path> <duration-seconds> [job-name]}"
JOB_NAME="${4:-cudn-density}"

RESULTS_FILE="/tmp/kube-burner-ssh-load-test-${UUID}.json"
SSH_USER="sshtest"
SSH_PORT=2222

for bin in oc jq ssh; do
    if ! command -v "$bin" >/dev/null 2>&1; then
        echo "cudn-ssh-load-test.sh: required binary '$bin' not found in PATH" >&2
        exit 1
    fi
done

if [ ! -r "$SSH_KEY" ]; then
    echo "cudn-ssh-load-test.sh: SSH private key not readable: $SSH_KEY" >&2
    exit 1
fi

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

TOTAL_PODS=$(wc -l < "${WORKDIR}/targets.txt" | tr -d ' ')
if [ "$TOTAL_PODS" -eq 0 ]; then
    echo "cudn-ssh-load-test.sh: no nginx-ssh server pods found for job '${JOB_NAME}'" >&2
    printf '{"totalAttempts":0,"successes":0,"failures":0,"durationSecs":0,"podsTargeted":0}\n' > "$RESULTS_FILE"
    exit 0
fi

echo "cudn-ssh-load-test.sh: starting load test against ${TOTAL_PODS} pod(s) for ${DURATION_SECS}s"

END_TIME=$(( $(date +%s) + DURATION_SECS ))
SUCCESS_COUNT=0
FAIL_COUNT=0

run_worker() {
    local ip="$1"
    local end="$2"
    local ok=0 fail=0

    while [ "$(date +%s)" -lt "$end" ]; do
        if ssh -n -p "$SSH_PORT" -o ConnectTimeout=10 -o StrictHostKeyChecking=no \
            -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
            -i "$SSH_KEY" "${SSH_USER}@${ip}" whoami >/dev/null 2>&1; then
            ok=$((ok + 1))
        else
            fail=$((fail + 1))
        fi
    done
    echo "${ok} ${fail}"
}
export -f run_worker
export SSH_KEY SSH_USER SSH_PORT

mapfile -t IPS < <(awk '{print $3}' "${WORKDIR}/targets.txt")

for ip in "${IPS[@]}"; do
    run_worker "$ip" "$END_TIME" > "${WORKDIR}/worker_${ip}.out" &
done

wait

for ip in "${IPS[@]}"; do
    if [ -f "${WORKDIR}/worker_${ip}.out" ]; then
        read -r ok fail < "${WORKDIR}/worker_${ip}.out"
        SUCCESS_COUNT=$((SUCCESS_COUNT + ok))
        FAIL_COUNT=$((FAIL_COUNT + fail))
    fi
done

TOTAL_ATTEMPTS=$((SUCCESS_COUNT + FAIL_COUNT))

printf '{"totalAttempts":%d,"successes":%d,"failures":%d,"durationSecs":%d,"podsTargeted":%d}\n' \
    "$TOTAL_ATTEMPTS" "$SUCCESS_COUNT" "$FAIL_COUNT" "$DURATION_SECS" "$TOTAL_PODS" > "$RESULTS_FILE"

echo "cudn-ssh-load-test.sh: load test complete — ${SUCCESS_COUNT} successes, ${FAIL_COUNT} failures out of ${TOTAL_ATTEMPTS} total attempts over ${DURATION_SECS}s (${TOTAL_PODS} pods targeted)"
echo "cudn-ssh-load-test.sh: results written to ${RESULTS_FILE}"
