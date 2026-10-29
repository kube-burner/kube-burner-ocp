#!/usr/bin/env bash
# Require a new, successful live migration for every VM in this iteration.
set -euo pipefail

action=${1:?expected prepare or verify}
namespace=${2:?expected namespace}
selector=${3:?expected iteration selector}
expected=${4:?expected VM count}
baseline=${5:?expected baseline file}

if [[ ! "$expected" =~ ^[1-9][0-9]*$ ]]; then
    echo "Migration VM count must be positive: $expected" >&2
    exit 1
fi

vms=$(kubectl get vm -n "$namespace" -l "$selector" -o json)
vmis=$(kubectl get vmi -n "$namespace" -l "$selector" -o json)

# Check both resources: selecting N VMIs alone does not prove the migrate
# operation's VirtualMachine selector matches the same N VMs.
if ! jq -en --argjson vms "$vms" --argjson vmis "$vmis" --argjson expected "$expected" '
    ($vms.items | map(.metadata.name) | sort) as $names |
    ($vmis.items | map(.metadata.name) | sort) as $instances |
    ($names | length) == $expected and $names == $instances and
    all($vmis.items[]; .status.phase == "Running" and (.status.nodeName // "") != "")
' >/dev/null; then
    echo "Migration requires exactly $expected running test VMs matching $selector" >&2
    exit 1
fi

case "$action" in
prepare)
    jq '[.items[] | {name: .metadata.name, uid: .metadata.uid,
        node: .status.nodeName, migrationUID: (.status.migrationState.migrationUid // "")}]' \
        <<< "$vmis" > "$baseline"
    echo "Migration expected=$expected; recorded all source VM identities"
    ;;
verify)
    # A previous successful migration, a replacement VMI, or a still-running
    # guest is insufficient. Require the same VMI and a new migration UID,
    # successful completion, and a move from its recorded source node.
    if ! jq -e --slurpfile before "$baseline" --argjson expected "$expected" '
        . as $current |
        ($before[0] | length) == $expected and
        all($before[0][]; . as $old |
            any($current.items[];
                .metadata.name == $old.name and .metadata.uid == $old.uid and
                (.status.migrationState.migrationUid // "") != "" and
                .status.migrationState.migrationUid != $old.migrationUID and
                .status.migrationState.completed == true and
                (.status.migrationState.failed // false) == false and
                .status.migrationState.sourceNode == $old.node and
                (.status.migrationState.targetNode // "") != "" and
                .status.migrationState.targetNode != $old.node and
                .status.nodeName == .status.migrationState.targetNode))
    ' <<< "$vmis" >/dev/null; then
        echo "Migration incomplete: expected $expected distinct VMs to move successfully" >&2
        exit 1
    fi
    echo "Migration expected=$expected completed=$expected"
    ;;
*)
    echo "Unknown migration check: $action" >&2
    exit 1
    ;;
esac
