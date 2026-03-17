#!/bin/bash

# Arguments passed from kube-burner
NUM_CUDN=${1:-72}

echo "Starting cleanup of $NUM_CUDN VRFs and EVPN configuration..."

# 1. Stop the Nginx Container
echo "Stopping Nginx container..."
podman stop shared-webserver 2>/dev/null
podman rm shared-webserver 2>/dev/null

# 2. Clear FRR BGP Configuration
# We remove the BGP VRF instances to stop route advertisements
echo "Clearing FRR BGP VRF configurations..."
FRR_CLEANUP_CMDS="configure terminal"
for i in $(seq 1 "$NUM_CUDN"); do
    FRR_CLEANUP_CMDS+="
no router bgp 64512 vrf vrf$i
no vrf vrf$i"
done
FRR_CLEANUP_CMDS+="
end
write memory"
podman exec frr vtysh -c "$FRR_CLEANUP_CMDS" 2>/dev/null

# 3. Remove Network Interfaces (SVIs and VRFs)
for i in $(seq 1 "$NUM_CUDN"); do
    VRF_NAME="vrf$i"
    SVI_NAME="${VRF_NAME}br"
    VLAN_ID=$((1099 + i))

    echo "Deleting $VRF_NAME and $SVI_NAME..."

    # Remove the SVI (VLAN interface)
    ip link delete "$SVI_NAME" 2>/dev/null

    # Remove the VRF device
    ip link delete "$VRF_NAME" 2>/dev/null

    # Clean up bridge VLAN entries
    bridge vlan del dev br0 vid "$VLAN_ID" self 2>/dev/null
    bridge vlan del dev vxlan0 vid "$VLAN_ID" 2>/dev/null
done

# 4. Remove Base Infrastructure
echo "Deleting base bridge and vxlan0..."
ip link delete vxlan0 2>/dev/null
ip link delete br0 2>/dev/null

# 5. Reset Shared Service Dummy (if it exists)
ip link delete shared-svc 2>/dev/null

# 6. Delete VTEP
echo "Deleting VTEP..."
oc delete vtep evpn-vtep 2>/dev/null || true

# 7. Cleanup frr-k8s directory
echo "Cleaning up frr-k8s directory..."
rm -rf frr-k8s 2>/dev/null || true

podman rm -f frr

echo "Cleanup complete."
