#!/bin/bash
# Propagate kubevirt.io/nodeName label from VMIs to VMs for a specific node
# Usage: ./propagate_node_labels.sh <node-name> <namespace>

set -e

NODE_NAME=$1
NAMESPACE=$2

if [ -z "$NODE_NAME" ] || [ -z "$NAMESPACE" ]; then
    echo "Usage: $0 <node-name> <namespace>"
    exit 1
fi

echo "Propagating kubevirt.io/nodeName=$NODE_NAME label to VMs in namespace $NAMESPACE"

# Get all VMIs running on the selected node
VM_NAMES=$(oc get vmi -n "$NAMESPACE" -l "kubevirt.io/nodeName=${NODE_NAME}" \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}')

if [ -z "$VM_NAMES" ]; then
    echo "No VMIs found on node $NODE_NAME in namespace $NAMESPACE"
    exit 0
fi

# Count VMs
VM_COUNT=$(echo "$VM_NAMES" | wc -w)
echo "Found $VM_COUNT VMs running on node $NODE_NAME"

# Label all VMs in a single API call
echo "Labeling VMs: $VM_NAMES"
oc label vm -n "$NAMESPACE" "$VM_NAMES" "kubevirt.io/nodeName=$NODE_NAME" --overwrite

echo "Successfully labeled $VM_COUNT VMs with kubevirt.io/nodeName=$NODE_NAME"
