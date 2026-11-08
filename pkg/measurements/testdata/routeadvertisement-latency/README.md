# Route advertisement integration tests

At runtime, the parent test downloads the latest CRDs from the `main` branch of
[`openshift/ovn-kubernetes`](https://github.com/openshift/ovn-kubernetes/tree/main):

- `helm/ovn-kubernetes/crds/k8s.ovn.org_routeadvertisements.yaml`
- `helm/ovn-kubernetes/crds/k8s.ovn.org_clusteruserdefinednetworks.yaml`

These manifests contain plain YAML and need no rendering. Downloads go into a
temporary directory, which is passed to the isolated child and deleted after the
test process exits. No CRD manifests are stored in the repository or embedded in
the test binary. Each run fetches the current branch contents rather than a
pinned commit.

Run `make test-ra-integration` from the repository root on Linux. This downloads
pinned envtest assets, builds the test binary, and runs it in a separate network
namespace. A peer namespace owns the addresses representing CUDN pods; its kernel
provides real ICMP replies. The API server and etcd run inside the measurement
namespace. Test setup needs HTTPS access to `raw.githubusercontent.com` for the
CRD downloads; the isolated networking cases need no OpenShift cluster or
external network.

```text
Parent test process
  Downloads CRDs over HTTPS into a temporary directory
  Starts the isolated child, passing the CRD directory
                   |
                   v
+------------------------------------+       +------------------------------------+
| Measurement network namespace      |       | Target network namespace           |
|                                    |       |                                    |
| lo: 127.0.0.1                      |       | lo: 10.200.0.2/32                  |
| envtest API server + etcd          |       | represents a CUDN pod              |
| raLatency uses the local API       |       | kernel answers ICMP                |
|                                    |       |                                    |
| ra-test: 192.0.2.1/30              |<----->| ra-peer: 192.0.2.2/30              |
|                                    | veth  |                                    |
| 10.200.0.0/24 via 192.0.2.2        |       | 20.0.0.0/8 via 192.0.2.1           |
| (added by harness for export)      |       | (return route for import)          |
|                                    |       |                                    |
| dummy0: 20.0.1.1/24 ...            |       | Echo replies can be disabled       |
|         20.0.12.1/24               |       | until the harness enables them     |
| (created by import workers)        |       |                                    |
+------------------------------------+       +------------------------------------+

Export: ra-test source IP -> target pod IP -> reply to ra-test
Import: dummy0 source IP  -> target pod IP -> reply via the return route
```

The export route is installed after the measurement subscribes to netlink route
updates. Import-only tests install it before starting the measurement. The dummy
interface addresses are created by the measurement itself. All links and routes
above belong to the isolated test namespaces.

The harness uses an isolated user namespace when run without root. If the host
disables user namespaces, use:

```sh
make test-ra-integration RA_INTEGRATION_RUNNER="sudo --preserve-env=KUBEBUILDER_ASSETS"
```

Ordinary `go test ./...` excludes these tests. The `integration` build tag and
Linux filename suffix select the suite. To invoke the binary directly, set
`KUBEBUILDER_ASSETS` to the directory containing the downloaded `etcd` and
`kube-apiserver` binaries. Missing assets or namespace permissions fail the suite
with an actionable error instead of silently skipping it.

Envtest runs no OVN controllers or kubelets. Tests create Pod/CUDN/RA objects,
update RA status, and program routes explicitly. They exercise the measurement's
discovery, watches, route monitoring, pinging, and cleanup rather than OVN's
network programming. Integration cases run sequentially because the measurement
currently uses package-level scenario configuration. Each case restores it.
