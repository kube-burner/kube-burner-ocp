#!/bin/bash
#
# Chaos actions for control-plane nodes during batch-churn load.
# Called as a kube-burner hook — runs in the background while churn is active.
#
# Usage:
#   ./chaos.sh <action> [delay_seconds] [repeat]
#
# Actions:
#   rollout          Force a rolling restart of kube-apiserver
#   kill-apiserver   Delete a random kube-apiserver pod
#   kill-node        Shut down a random master node
#   kill-etcd        Delete a random etcd pod (forces leader election)
#
# The optional delay_seconds (default: 0) waits before executing,
# letting the cluster reach peak load first.

set -euo pipefail

ACTION="${1:?Usage: chaos.sh <rollout|kill-apiserver|kill-node|kill-etcd> [delay_seconds] [repeat]}"
DELAY="${2:-0}"
REPEAT="${3:-1}"

if [ "$DELAY" -gt 0 ]; then
  echo "[chaos] waiting ${DELAY}s before executing ${ACTION}"
  sleep "$DELAY"
fi

pick_master_node() {
  for pod in $(oc get pods -n openshift-etcd -l app=etcd -o jsonpath='{.items[*].metadata.name}'); do
    IS_LEADER=$(oc exec -n openshift-etcd "$pod" -c etcd -- \
      etcdctl endpoint status --write-out=json 2>/dev/null \
      | python3 -c "import sys,json; d=json.load(sys.stdin)[0]; print('true' if d['Status']['header']['member_id']==d['Status']['leader'] else 'false')" 2>/dev/null)
    if [ "$IS_LEADER" = "true" ]; then
      LEADER_NODE=$(oc get pod -n openshift-etcd "$pod" -o jsonpath='{.spec.nodeName}')
      echo "[chaos] etcd leader is on ${LEADER_NODE}" >&2
      echo "$LEADER_NODE"
      return
    fi
  done
  echo "[chaos] could not determine etcd leader, falling back to first master" >&2
  oc get nodes -l node-role.kubernetes.io/master -o jsonpath='{.items[0].metadata.name}'
}

for i in $(seq 1 "$REPEAT"); do
echo "[chaos] iteration ${i}/${REPEAT}"

case "$ACTION" in
  rollout)
    echo "[chaos] forcing kube-apiserver rolling restart"
    oc patch kubeapiserver/cluster --type merge \
      -p "{\"spec\":{\"forceRedeploymentReason\":\"chaos-$(date +%s)\"}}"
    echo "[chaos] rollout triggered — API servers will restart one at a time"
    ;;

  kill-apiserver)
    NODE=$(pick_master_node)
    echo "[chaos] deleting kube-apiserver pod on node ${NODE}"
    oc delete pod -n openshift-kube-apiserver \
      -l app=openshift-kube-apiserver \
      --field-selector "spec.nodeName=${NODE}"
    echo "[chaos] kube-apiserver pod killed on ${NODE}, waiting for recovery..."
    oc wait pod -n openshift-kube-apiserver \
      -l app=openshift-kube-apiserver \
      --field-selector "spec.nodeName=${NODE}" \
      --for=condition=Ready --timeout=600s
    echo "[chaos] kube-apiserver pod is Ready on ${NODE}"
    ;;

  kill-node)
    NODE=$(pick_master_node)
    echo "[chaos] rebooting master node ${NODE}"
    oc debug "node/${NODE}" -- chroot /host reboot || true
    echo "[chaos] reboot sent to ${NODE}, waiting for recovery..."
    oc wait node/"${NODE}" --for=condition=Ready --timeout=900s
    echo "[chaos] ${NODE} is Ready again"
    ;;

  kill-etcd)
    NODE=$(pick_master_node)
    echo "[chaos] deleting etcd pod on node ${NODE}"
    oc delete pod -n openshift-etcd \
      -l app=etcd \
      --field-selector "spec.nodeName=${NODE}"
    echo "[chaos] etcd pod killed on ${NODE}, waiting for recovery..."
    oc wait pod -n openshift-etcd \
      -l app=etcd \
      --field-selector "spec.nodeName=${NODE}" \
      --for=condition=Ready --timeout=600s
    echo "[chaos] etcd pod is Ready on ${NODE} — leader election forced"
    ;;

  *)
    echo "[chaos] unknown action: ${ACTION}"
    echo "valid actions: rollout, kill-apiserver, kill-node, kill-etcd"
    exit 1
    ;;
esac
done
