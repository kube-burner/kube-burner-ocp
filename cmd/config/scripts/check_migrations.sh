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

# A large VM/VMI list can exceed Linux's per-argument limit when passed to
# jq through --argjson. Keep the API responses in private files instead.
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
vms_file="$tmp_dir/vms.json"
vmis_file="$tmp_dir/vmis.json"
kubectl get vm -n "$namespace" -l "$selector" -o json > "$vms_file"
kubectl get vmi -n "$namespace" -l "$selector" -o json > "$vmis_file"

# Check both resources: selecting N VMIs alone does not prove the migrate
# operation's VirtualMachine selector matches the same N VMs.
if ! jq -es --argjson expected "$expected" '
    .[0] as $vms | .[1] as $vmis |
    ($vms.items | map(.metadata.name) | sort) as $names |
    ($vmis.items | map(.metadata.name) | sort) as $instances |
    ($names | length) == $expected and $names == $instances and
    all($vmis.items[]; .status.phase == "Running" and (.status.nodeName // "") != "")
' "$vms_file" "$vmis_file" >/dev/null; then
    echo "Migration requires exactly $expected running test VMs matching $selector" >&2
    exit 1
fi

case "$action" in
prepare)
    jq '[.items[] | {name: .metadata.name, uid: .metadata.uid,
        node: .status.nodeName, migrationUID: (.status.migrationState.migrationUid // "")}]' \
        "$vmis_file" > "$baseline"
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
    ' "$vmis_file" >/dev/null; then
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
