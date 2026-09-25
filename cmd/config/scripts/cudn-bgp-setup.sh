#!/usr/bin/env bash
#
# Pre-job hook for cudn-density (--bgp). Sets up the local FRR container on
# the external host and configures BGP peering with the cluster.
#
# Note: The cluster-side enablement (oc patch Network.operator + CRD install)
# is done in Go code before kube-burner starts, so that the RouteAdvertisements
# CRD is available for template validation.
#
# Usage: cudn-bgp-setup.sh <external-host-ip>

set -euo pipefail

EXTERNAL_HOST_IP="${1:?usage: cudn-bgp-setup.sh <external-host-ip>}"
BGP_AS=64512

for bin in git podman oc; do
    if ! command -v "$bin" >/dev/null 2>&1; then
        echo "cudn-bgp-setup.sh: required binary '$bin' not found in PATH" >&2
        exit 1
    fi
done

echo "cudn-bgp-setup.sh: cloning frr-k8s and running demo.sh..."
rm -rf /tmp/frr-k8s
git clone -b ovnk-bgp https://github.com/jcaamano/frr-k8s /tmp/frr-k8s
pushd /tmp/frr-k8s/hack/demo >/dev/null
./demo.sh
popd >/dev/null

echo "cudn-bgp-setup.sh: applying FRRConfiguration with toReceive..."
cat <<EOF | oc apply -n openshift-frr-k8s -f -
apiVersion: frrk8s.metallb.io/v1beta1
kind: FRRConfiguration
metadata:
  name: receive-all
spec:
  bgp:
    routers:
    - asn: 64512
      neighbors:
      - address: ${EXTERNAL_HOST_IP}
        asn: 64512
        disableMP: true
        toReceive:
          allowed:
            mode: all
EOF

echo "cudn-bgp-setup.sh: configuring FRR to redistribute routes..."
podman exec frr vtysh -c "
configure terminal
router bgp ${BGP_AS}
redistribute static
redistribute connected
end
write
"

echo "cudn-bgp-setup.sh: BGP setup complete. Verifying peering..."
podman exec frr vtysh -c "show bgp summary"
