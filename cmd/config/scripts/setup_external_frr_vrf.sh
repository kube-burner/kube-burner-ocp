#!/bin/bash

# Arguments passed from kube-burner
NUM_CUDN=${1:-72}
EXTERNAL_WEBSERVER_IP=${2:-""}
L3VNI_START=${3:-100}

if [[ -z "$EXTERNAL_WEBSERVER_IP" ]]; then
    echo "ERROR: EXTERNAL_WEBSERVER_IP is required as second argument"
    exit 1
fi

BGP_AS=64512

echo "Setting up external FRR VRF with:"
echo "  - Number of CUDNs: $NUM_CUDN"
echo "  - External webserver IP: $EXTERNAL_WEBSERVER_IP"
echo "  - L3 VNI start: $L3VNI_START"

# Get OCP node IPs
echo "Detecting OCP node IPs..."
NODE_IPS=$(oc get nodes -o jsonpath='{.items[*].status.addresses[?(@.type=="InternalIP")].address}' \
  | grep -Po '(?<=\s|^)[0-9.]+')

# Get the first node IP to determine the subnet
FIRST_NODE_IP=$(echo "$NODE_IPS" | head -1)
echo "First node IP: $FIRST_NODE_IP"

# Find local interface that can reach the OCP nodes and get its CIDR
echo "Finding local interface on OCP node network..."
LOCAL_IP=""
NODE_SUBNET_CIDR=""

# Get all local IPs with their CIDRs
while read -r line; do
    iface=$(echo "$line" | awk '{print $1}')
    ip_cidr=$(echo "$line" | awk '{print $2}')
    ip_addr=$(echo "$ip_cidr" | cut -d'/' -f1)
    cidr_mask=$(echo "$ip_cidr" | cut -d'/' -f2)

    # Skip loopback
    [[ "$iface" == "lo" ]] && continue

    # Check if this IP is on the same network as the first node
    # Convert IPs to integers for comparison based on CIDR mask
    IFS='.' read -r i1 i2 i3 _ <<< "$ip_addr"
    IFS='.' read -r n1 n2 n3 _ <<< "$FIRST_NODE_IP"

    case "$cidr_mask" in
        8)
            if [[ "$i1" == "$n1" ]]; then
                LOCAL_IP="$ip_addr"
                NODE_SUBNET_CIDR="${i1}.0.0.0/${cidr_mask}"
                echo "Found local IP on /${cidr_mask} network: $LOCAL_IP (interface: $iface)"
                break
            fi
            ;;
        16)
            if [[ "$i1" == "$n1" && "$i2" == "$n2" ]]; then
                LOCAL_IP="$ip_addr"
                NODE_SUBNET_CIDR="${i1}.${i2}.0.0/${cidr_mask}"
                echo "Found local IP on /${cidr_mask} network: $LOCAL_IP (interface: $iface)"
                break
            fi
            ;;
        24)
            if [[ "$i1" == "$n1" && "$i2" == "$n2" && "$i3" == "$n3" ]]; then
                LOCAL_IP="$ip_addr"
                NODE_SUBNET_CIDR="${i1}.${i2}.${i3}.0/${cidr_mask}"
                echo "Found local IP on /${cidr_mask} network: $LOCAL_IP (interface: $iface)"
                break
            fi
            ;;
        *)
            # For other CIDR masks, do a more general check
            # Compare based on the number of matching octets implied by the mask
            if [[ "$cidr_mask" -ge 24 ]]; then
                if [[ "$i1" == "$n1" && "$i2" == "$n2" && "$i3" == "$n3" ]]; then
                    LOCAL_IP="$ip_addr"
                    NODE_SUBNET_CIDR="${i1}.${i2}.${i3}.0/${cidr_mask}"
                    echo "Found local IP on /${cidr_mask} network: $LOCAL_IP (interface: $iface)"
                    break
                fi
            elif [[ "$cidr_mask" -ge 16 ]]; then
                if [[ "$i1" == "$n1" && "$i2" == "$n2" ]]; then
                    LOCAL_IP="$ip_addr"
                    NODE_SUBNET_CIDR="${i1}.${i2}.0.0/${cidr_mask}"
                    echo "Found local IP on /${cidr_mask} network: $LOCAL_IP (interface: $iface)"
                    break
                fi
            elif [[ "$cidr_mask" -ge 8 ]]; then
                if [[ "$i1" == "$n1" ]]; then
                    LOCAL_IP="$ip_addr"
                    NODE_SUBNET_CIDR="${i1}.0.0.0/${cidr_mask}"
                    echo "Found local IP on /${cidr_mask} network: $LOCAL_IP (interface: $iface)"
                    break
                fi
            fi
            ;;
    esac
done < <(ip -4 -o addr show | awk '{print $2, $4}')

if [[ -z "$LOCAL_IP" ]]; then
    echo "ERROR: Could not find local IP on OCP node network"
    exit 1
fi

echo "Using LOCAL_IP: $LOCAL_IP"
echo "Using NODE_SUBNET_CIDR: $NODE_SUBNET_CIDR"

# Clone and setup FRR
echo "Cloning and setting up FRR..."
rm -rf frr-k8s
podman rm -f frr
git clone -b ovnk-bgp https://github.com/jcaamano/frr-k8s
sed -i 's|quay.io/frrouting/frr:.*|quay.io/frrouting/frr:10.4.1|' frr-k8s/hack/demo/demo.sh
pushd frr-k8s/hack/demo || exit; ./demo.sh; popd || exit
oc apply -n openshift-frr-k8s -f frr-k8s/hack/demo/configs/receive_all.yaml

# Configure EVPN BGP neighbors
echo "Configuring EVPN BGP neighbors..."
EVPN_CMDS="configure terminal
router bgp $BGP_AS
address-family l2vpn evpn
advertise-all-vni"
for ip in $NODE_IPS; do
  EVPN_CMDS="$EVPN_CMDS
neighbor $ip activate
neighbor $ip route-reflector-client"
done
EVPN_CMDS="$EVPN_CMDS
end
write memory"

if ! podman exec frr vtysh -c "$EVPN_CMDS"; then
      echo "vtysh returned non-zero for advertise-all-vni, but continuing..."
fi

# Create VTEP with detected node subnet CIDR
echo "Creating VTEP with CIDR: $NODE_SUBNET_CIDR"
cat <<EOF | oc apply -f -
apiVersion: k8s.ovn.org/v1
kind: VTEP
metadata:
  name: evpn-vtep
spec:
  mode: Unmanaged
  cidrs:
    - $NODE_SUBNET_CIDR
EOF

# Global Kernel Tuning for VRF/Anycast
echo "Tuning kernel for Anycast VRF performance..."
sysctl -w net.ipv4.tcp_l3mdev_accept=1
sysctl -w net.ipv4.udp_l3mdev_accept=1
sysctl -w net.ipv4.ip_forward=1
sysctl -w net.ipv4.conf.all.forwarding=1
sysctl -w net.ipv4.conf.all.rp_filter=2

# Base Bridge and VXLAN Setup
echo "Initializing Bridge (br0) and VXLAN (vxlan0)..."
ip link add br0 type bridge vlan_filtering 1 vlan_default_pvid 0
ip link set br0 addrgenmode none
ip link add vxlan0 type vxlan dstport 4789 local "$LOCAL_IP" nolearning external vnifilter
ip link set vxlan0 addrgenmode none master br0
bridge link set dev vxlan0 vlan_tunnel on neigh_suppress on learning off
ip link set br0 up
ip link set vxlan0 up

# Loop to create VRFs with Anycast IPs
# VNI formula matches cudn.yml: $l3vni := add $.l3vni_start $.Iteration (0-indexed)
# So for i=1, VNI_ID = L3VNI_START + 0 = L3VNI_START (matching Iteration=0)
echo "Creating $NUM_CUDN VRFs with Anycast IP: $EXTERNAL_WEBSERVER_IP (L3 VNI starting at $L3VNI_START)"
FRR_CMDS=""
for i in $(seq 1 "$NUM_CUDN"); do
    VRF_NAME="vrf$i"
    VLAN_ID=$((1099 + i))   # 1100, 1101, ...
    # VNI_ID matches cudn.yml: l3vni = l3vni_start + Iteration (0-indexed)
    # i starts from 1, so we use (i-1) to match 0-indexed Iteration
    VNI_ID=$((L3VNI_START + i - 1))
    TABLE_ID=$VLAN_ID

    echo "Configuring $VRF_NAME (VNI $VNI_ID) with Anycast IP..."

    # Create VRF
    ip link add "$VRF_NAME" type vrf table "$TABLE_ID"
    ip link set "$VRF_NAME" up

    # Bridge/VLAN Mapping
    bridge vlan add dev br0 vid "$VLAN_ID" self
    bridge vlan add dev vxlan0 vid "$VLAN_ID"
    bridge vni add dev vxlan0 vni "$VNI_ID"
    bridge vlan add dev vxlan0 vid "$VLAN_ID" tunnel_info id "$VNI_ID"

    # Create SVI and Assign Anycast IP
    SVI_NAME="${VRF_NAME}br"
    ip link add "$SVI_NAME" link br0 type vlan id "$VLAN_ID"
    ip link set "$SVI_NAME" master "$VRF_NAME"

    # Assign the IP - Linux allows the same IP on different VRF SVIs
    ip addr add "${EXTERNAL_WEBSERVER_IP}/32" dev "$SVI_NAME"

    # ARP Suppression (Prevents IP conflicts on the bridge)
    sysctl -w "net.ipv4.conf.${SVI_NAME}.arp_ignore=1"
    sysctl -w "net.ipv4.conf.${SVI_NAME}.arp_announce=2"

    ip link set "$SVI_NAME" up

    # Build FRR Config string
    FRR_CMDS+="
configure terminal
vrf $VRF_NAME
 vni $VNI_ID
exit-vrf
router bgp $BGP_AS vrf $VRF_NAME
 address-family ipv4 unicast
  network $EXTERNAL_WEBSERVER_IP/32
 exit-address-family
 address-family l2vpn evpn
  advertise ipv4 unicast
 exit-address-family
end"
done
FRR_CMDS+="
write memory"
# Apply FRR Config
echo "Applying FRR/BGP configurations..."
if ! podman exec frr vtysh -c "$FRR_CMDS"; then
      echo "vtysh returned non-zero, but continuing..."
fi
# Start Nginx on Host Network
echo "Starting Nginx in Host Network mode..."
podman stop shared-webserver 2>/dev/null || true
podman rm shared-webserver 2>/dev/null || true

podman run -d --name shared-webserver \
  --net=host \
  docker.io/library/nginx:latest

echo "Setup Complete."
echo "  - Service $EXTERNAL_WEBSERVER_IP is now active in $NUM_CUDN VRFs"
echo "  - L3 VNI range: $L3VNI_START to $((L3VNI_START + NUM_CUDN - 1))"
