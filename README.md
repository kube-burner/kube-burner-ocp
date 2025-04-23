# OpenShift Wrapper

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/10060/badge)](https://www.bestpractices.dev/projects/10060)

This plugin is a very opinionated OpenShift wrapper designed to simplify the execution of different workloads in this Kubernetes distribution.

Executed with `kube-burner-ocp`, it looks like:

```console
$ kube-burner-ocp --help
kube-burner plugin designed to be used with OpenShift clusters as a quick way to run well-known workloads

Usage:
  kube-burner-ocp [command]

Available Commands:
  cluster-density-ms             Runs cluster-density-ms workload
  cluster-density-v2             Runs cluster-density-v2 workload
  cluster-health                 Checks for ocp cluster health
  completion                     Generate the autocompletion script for the specified shell
  crd-scale                      Runs crd-scale workload
  help                           Help about any command
  index                          Runs index sub-command
  init                           Runs custom workload
  networkpolicy-matchexpressions Runs networkpolicy-matchexpressions workload
  networkpolicy-matchlabels      Runs networkpolicy-matchlabels workload
  networkpolicy-multitenant      Runs networkpolicy-multitenant workload
  node-density                   Runs node-density workload
  node-density-cni               Runs node-density-cni workload
  node-density-heavy             Runs node-density-heavy workload
  pvc-density                    Runs pvc-density workload
  udn-density-l3-pods            Runs udn-density-l3-pods workload
  version                        Print the version number of kube-burner
  virt-capacity-benchmark        Runs capacity-benchmark workload
  virt-density                   Runs virt-density workload
  web-burner-cluster-density     Runs web-burner-cluster-density workload
  web-burner-init                Runs web-burner-init workload
  web-burner-node-density        Runs web-burner-node-density workload

Flags:
      --alerting                  Enable alerting (default true)
      --burst int                 Burst (default 20)
      --es-index string           Elastic Search index
      --es-server string          Elastic Search endpoint
      --extract                   Extract workload in the current directory
      --gc                        Garbage collect created resources (default true)
      --gc-metrics                Collect metrics during garbage collection
      --local-indexing            Enable local indexing
      --metrics-endpoint string   YAML file with a list of metric endpoints
      --profile-type string       Metrics profile to use, supported options are: regular, reporting or both (default "both")
      --qps int                   QPS (default 20)
      --timeout duration          Benchmark timeout (default 4h0m0s)
      --user-metadata string      User provided metadata file, in YAML format
      --uuid string               Benchmark UUID (default "0827cb6a-9367-4f0b-b11c-75030c69479e")
      --log-level string          Allowed values: debug, info, warn, error, fatal (default "info")
  -h, --help                      help for kube-burner-ocp
```

## Documentation

Documentation is [available here](https://kube-burner.github.io/kube-burner-ocp/)

## Usage

Some of the benefits the OCP wrapper provides are:

- Simplified execution of the supported workloads. (Only some flags are required)
- Adds OpenShift metadata to generated jobSummary and a small subset of metadata fields to the remaining metrics.
- Prevents modifying configuration files to tweak some of the parameters of the workloads.
- Discovers the Prometheus URL and authentication token, so the user does not have to perform those operations before using them.
- Workloads configuration is directly embedded in the binary.

Running node-density with 100 pods per node

```console
kube-burner-ocp node-density --pods-per-node=100
```

With the command above, the wrapper will calculate the required number of pods to deploy across all worker nodes of the cluster.

## Multiple endpoints support

The flag `--metrics-endpoint` can be used to interact with multiple Prometheus endpoints
For example:

```console
kube-burner-ocp cluster-density-v2 --iterations=1 --churn-duration=2m0s --churn-cycles=2 --es-index kube-burner --es-server https://www.esurl.com:443 --metrics-endpoint metrics-endpoints.yaml
```

### metrics-endpoints.yaml

```yaml
- endpoint: prometheus-k8s-openshift-monitoring.apps.rook.devshift.org
  metrics:
    - metrics.yml
  alerts:
    - alerts.yml
  indexer:
      esServers: ["{{.ES_SERVER}}"]
      insecureSkipVerify: true
      defaultIndex: {{.ES_INDEX}}
      type: opensearch
- endpoint: https://prometheus-k8s-openshift-monitoring.apps.rook.devshift.org
  token: {{ .TOKEN }}
  metrics:
    - metrics.yml
  indexer:
      esServers: ["{{.ES_SERVER}}"]
      insecureSkipVerify: true
      defaultIndex: {{.ES_INDEX}}
      type: opensearch
```

`.TOKEN` can be captured by running `TOKEN=$(oc create token -n openshift-monitoring prometheus-k8s)`

!!! Note

    Avoid passing absolute path of the file with --metrics-endpoint option

    Metric profile names specified against `metrics` key should be unique and shouldn't overlap with the existing ones. A metric profile will be looked up in this directory [config](https://github.com/kube-burner/kube-burner-ocp/tree/main/cmd/config) first for the sake of simplicity and if it doesn't exist, will fallback to our specified path. So in order for our own metric profile to get picked up, we will need to specify its absolute path or name differently whenever there is an overlap with the existing ones.

## Cluster density workloads

This workload family is a control-plane density focused workload that that creates different objects across the cluster. There are 2 different variants [cluster-density-v2](#cluster-density-v2) and [cluster-density-ms](#cluster-density-ms).

Each iteration of these create a new namespace, the three support similar configuration flags. Check them out from the subcommand help.

!!! Info
    Workload churning of 1h is enabled by default in the `cluster-density` workloads; you can disable it by passing `--churn=false` to the workload subcommand.

### cluster-density-v2

Each iteration creates the following objects in each of the created namespaces:

- 1 image stream.
- 1 build. The OCP internal container registry must be set-up previously because the resulting container image will be pushed there.
- 3 deployments with two pod 2 replicas (nginx) mounting 4 secrets, 4 config maps, and 1 downward API volume each.
- 2 deployments with two pod 2 replicas (curl) mounting 4 Secrets, 4 config maps and 1 downward API volume each. These pods have configured a readiness probe that makes a request to one of the services and one of the routes created by this workload every 10 seconds.
- 5 services, each one pointing to the TCP/8080 port of one of the nginx deployments.
- 2 edge routes pointing to the to first and second services respectively.
- 10 secrets containing a 2048-character random string.
- 10 config maps containing a 2048-character random string.
- 3 network policies:
    - deny-all traffic
    - allow traffic from client/nginx pods to server/nginx pods
    - allow traffic from openshift-ingress namespace (where routers are deployed by default) to the namespace

### cluster-density-ms

Lightest version of this workload family, each iteration the following objects in each of the created namespaces:

- 1 image stream.
- 4 deployments with two pod replicas (pause) mounting 4 secrets, 4 config maps, and 1 downward API volume each.
- 2 services, each one pointing to the TCP/8080 and TCP/8443 ports of the first and second deployment respectively.
- 1 edge route pointing to the to first service.
- 20 secrets containing a 2048-character random string.
- 10 config maps containing a 2048-character random string.

## Node density workloads

The workloads of this family create a single namespace with a set of pods, deployments, and services depending on the workload.

### node-density

This workload is meant to fill with pause pods all the worker nodes from the cluster. It can be customized with the following flags. This workload is usually used to measure the Pod's ready latency KPI.

### node-density-cni

It creates two deployments, a client/curl and a server/nxing, and 1 service backed by the previous server pods. The client application has configured an startup probe that makes requests to the previous service every second with a timeout of 600s.

Note: This workload calculates the number of iterations to create from the number of nodes and desired pods per node.  In order to keep the test scalable and performant, chunks of 1000 iterations will by broken into separate namespaces, using the config variable `iterationsPerNamespace`.

### node-density-heavy

Creates two deployments, a postgresql database, and a simple client that performs periodic insert queries (configured through liveness and readiness probes) on the previous database and a service that is used by the client to reach the database.

Note: this workload calculates the number of iterations to create from the number of nodes and desired pods per node.  In order to keep the test scalable and performant, chunks of 1000 iterations will by broken into separate namespaces, using the config variable `iterationsPerNamespace`.

### udn-density-l3-pods

For User-Defined Network (UDN) L3 segmentation testing. It creates two deployments, a client/curl and a server/nxing.

## Network Policy workloads

Network policy scale testing tooling involved  2 components:
1. Template to include all network policy configuration options
2. Latency measurement through connection testing

A network policy defines the rules for ingress and egress traffic between pods in  local and remote namespaces. These remote namespace addresses can be configured using a combination of namespace and pod selectors, CIDRs, ports, and port ranges. Given that network policies offer a wide variety of configuration options, we developed a unified template that incorporates all these configuration parameters. Users can specify the desired count for each option.

```console
spec:
  podSelector:
    matchExpressions:
    - key: num
      operator: In
      values:
      - "1"
      - "2"
  ingress:
  - from:
    - namespaceSelector:
        matchExpressions:
        - key: kubernetes.io/metadata.name
          operator: In
          values:
          - network-policy-perf-13
          - network-policy-perf-14
      podSelector:
       matchExpressions:
       - key: num
         operator: In
         values:
         - "1"
         - "2"
    ports:
    - port: 8080
      protocol: TCP

```

### Scale Testing and Unique ACL Flows
In our scale tests, we aim to create between 10 to 100 network policies within a single namespace. The primary focus is on preventing duplicate configuration options, which ensures that each network policy generates unique Access Control List (ACL) flows. To achieve this, we carefully designed our templating approach based on the following considerations:

**Round-Robin Assignment:** We use a round-robin strategy to distribute
1. remote namespaces among ingress and egress rules across kube burner job iterations
2. remote namespaces among ingress and egress rules in the same kube burner job iteration

This ensures that we don’t overuse the same remote namespaces in a single iteration or among multiple interations. For instance, if namespace-1 uses namespace-2 and namespace-3 as its remote namespaces, then namespace-2 will start using namespace-4 and namespace-5 as remote namespaces in the next iteration.

**Unique Namespace and Pod Combinations:** To avoid redundant flows, the templating system generates unique combinations of remote namespaces and pods for each network policy. Initially, we iterate through the list of remote namespaces, and once all remote namespaces are exhausted, we move on to iterate through the remote pods. This method ensures that every network policy within a namespace is assigned a distinct combination of remote namespaces and remote pods, avoiding duplicate pairs.

**Templating Logic**
Our templating logic is implemented as follows:
``` console
// Iterate over the list of namespaces to configure network policies.
for namespace := namespaces {

  // Each network policy uses a combination of a remote namespace and a remote pod to allow traffic.
  for networkPolicy := networkPolicies {

    /*
    Iterate through the list of remote pods. Once all remote namespaces are exhausted,
    continue iterating through the remote pods to ensure unique namespace/pod combinations.
    */
    for i, remotePod := range remotePods {
        // Stop when we reach the maximum number of remote pods allowed.
        if i == num_remote_pods {
            break
        }

        // Iterate through the list of remote namespaces to pair with the remote pod.
        for idx, remoteNamespace := range remoteNamespaces {
            // Combine the remote namespace and pod into a unique pair for ACL configuration.
            combine := fmt.Sprintf("%s:%s", remoteNamespace, remotePod)

            // Stop iterating once we’ve exhausted the allowed number of remote namespaces.
            if idx == num_remote_namespace {
                break
            }
        }
    }
  }
}

```

**CIDRs and Port Ranges**
We apply the same round-robin and unique combination logic to CIDRs and port ranges, ensuring that these options are not reused in network policies within the same namespace.

**Connection Testing Support**
kube-burner measures network policy latency through connection testing. Currently, all pods are configured to listen on port 8080. As a result, client pods will send requests to port 8080 during testing.

Note: Egress rules should not be enabled for network policy latency measurement connection testing.

## EgressIP workloads

This workload creates an egress IP for the client pods. SDN (OVN) will use egress IP for the traffic from client pods to external server instead of default node IP.

Each iteration creates the following objects in each of the created namespaces:

- 1 deployment with the configured number of client pod replicas. Client pod runs the quay.io/cloud-bulldozer/eipvalidator app which periodically sends http request to the configured "EXT_SERVER_HOST" server at an "DELAY_BETWEEN_REQ_SEC" interval with a request timeout of "REQ_TIMEOUT_SEC" seconds. Client pod then validates if the body of the response has configured "EGRESS_IPS". Once the client pod starts running and after receiving first succesful response with configured "EGRESS_IPS", it sets "eip_startup_latency_total" prometheus metric.
- 1 EgressIP object. EgressIP object is cluster scoped. EgressIP object will have number of egress IP addresses which user specified through "addresses-per-iteration" cli option. kube-burner generates these addresses for the egressIP object from the egress IP list provided by kube-burner-ocp. OVN applies egressIPs to the pods in the current job iteration because of "namespaceSelector" and "podSelector" fields in the egressIP object.

Note: User has to manually create the external server or use the e2e-benchmarking(https://github.com/cloud-bulldozer/e2e-benchmarking/tree/master/workloads/kube-burner-ocp-wrapper#egressip) which deploys external server and runs the workload with required configuration.

Running 1 iteration with 1 egress IP address per iteration (or egressIP object).

```console
kube-burner-ocp egressip --addresses-per-iteration=1 --iterations=1 --external-server-ip=10.0.34.43
```

With the command above, each namespace has one pod with a dedicated egress IP. OVN will use this dedicated egress IP for the http requests from client pod's to 10.0.34.43.

## Web-burner workloads

This workload is meant to emulate some telco specific workloads. Before running *web-burner-node-density* or *web-burner-cluster-density* load the environment with *web-burner-init* first (without the garbage collection flag: `--gc=false`).

Pre-requisites:

- At least two worker nodes
- At least one of the worker nodes must have the `node-role.kubernetes.io/worker-spk` label

### web-burner-init

- 35 (macvlan/sriov) networks for 35 lb namespace
- 35 lb-ns
  - 1 frr config map, 4 emulated lb pods on each namespace
-  35 app-ns
	- 1 emulated lb pod on each namespace for bfd session

### web-burner-node-density

- 35 app-ns
  - 3 app pods and services on each namespace
- 35 normal-ns
	- 1 service with 60 normal pod endpoints on each namespace

### web-burner-cluster-density

- 20 normal-ns
	- 30 configmaps, 38 secrets, 38 normal pods and services, 5 deployments with 2 replica pods on each namespace
- 35 served-ns
  - 3 app pods on each namespace
- 2 app-served-ns
	- 1 service(15 ports) with 84 pod endpoints, 1 service(15 ports) with 56 pod endpoints, 1 service(15 ports) with 25 pod endpoints
	- 3 service(15 ports each) with 24 pod endpoints, 3 service(15 ports each) with 14 pod endpoints
	- 6 service(15 ports each) with 12 pod endpoints, 6 service(15 ports each) with 10 pod endpoints, 6 service(15 ports each) with 9 pod endpoints
	- 12 service(15 ports each) with 8 pod endpoints, 12 service(15 ports each) with 6 pod endpoints, 12 service(15 ports each) with 5 pod endpoints
	- 29 service(15 ports each) with 4 pod endpoints, 29 service(15 ports each) with 6 pod endpoints

## Core RDS workloads

The telco core reference design specification (RDS) describes OpenShift Container Platform clusters running on commodity hardware that can support large scale telco applications including control plane and some centralized data plane functions. It captures the recommended, tested, and supported configurations to get reliable and repeatable performance for clusters running the telco core profile.

Pre-requisites:
 - A **PerformanceProfile** with isolated and reserved cores, 1G hugepages and and `topologyPolicy=single-numa-node`. Hugepages should be allocated in the first NUMA node (the one that would be used by DPDK deployments):
     ```yaml
      hugepages:
      defaultHugepagesSize: 1G
      pages:
      - count: 160
        node: 0
        size: 1G
      - count: 6
        node: 1
        size: 1G
     ```
 - **MetalLB operator** limiting speaker pods to specific nodes (aprox. 10%, 12 in the case of 120 node iterations with the corresponding ***worker-metallb*** label):
     ```yaml
     apiVersion: metallb.io/v1beta1
     kind: MetalLB
     metadata:
       name: metallb
       namespace: metallb-system
     spec:
       nodeSelector:
         node-role.kubernetes.io/worker-metallb: ""
       speakerTolerations:
       - key: "Example"
         operator: "Exists"
         effect: "NoExecute"
     ```
 - **SRIOV operator** with its corresponding *SriovNetworkNodePolicy*
 - Some nodes (i.e.: 25% of them) with the ***worker-dpdk*** label to host the DPDK pods, i.e.:
     ```
     $ kubectl label node worker1 node-role.kubernetes.io/worker-dpdk=
     ```

Object count:
| Iterations / nodes / namespaces   | 1    | 120                                 |
| --------------------------------- | ---- | ----------------------------------- |
| configmaps                        | 30   | 3600                                |
| deployments_best_effort           | 25   | 3000                                |
| deployments_dpdk                  | 2    | 240 (assuming 24 worker-dpdk nodes) |
| endpoints (210x service)          | 4200 | 504000                              |
| endpoints lb (90 x service)       | 90   | 10800                               |
| networkPolicy                     | 3    | 360                                 |
| namespaces                        | 1    | 120                                 |
| pods_best_effort (2 x deployment) | 50   | 6000                                |
| pods_dpdk (1 x deployment)        | 2    | 240 (assuming 24 worker-dpdk nodes) |
| route                             | 2    | 240                                 |
| services                          | 20   | 2400                                |
| services (lb)                     | 1    | 120                                 |
| secrets                           | 42   | 5040                                |


Input parameters specific to the workload:
| Parameter           | Description                                                                                      | Default value |
| ------------------- | ------------------------------------------------------------------------------------------------ | ------------- |
| dpdk-cores          | Number of cores assigned for each DPDK pod (should fill all the isolated cores of one NUMA node) | 2             |
| performance-profile | Name of the performance profile implemented on the cluster                                       | default       |


## Virt Workloads

This workload family is a focused on Virtualization creating different objects across the cluster.

The different variants are:
- [virt-density](#virt-density)
- [virt-udn-density](#virt-density-udn)
- [virt-capacity-benchmark](#virt-capacity-benchmark).
- [virt-clone](#virt-clone)
- [virt-ephemeral-restart](#virt-ephemeral-restart)

### Virt Density

Similar to node-density, fills with VirtualMachines the worker nodes of the cluster (**kubevirt/OpenShift Virtualization is required** to run this workload). Meant to detect issues derived from spinning up high amounts VMs in a short amount of time and to track runningthe latencies of the different VM bootstrap stages.

### Virt Density Udn

Similar to udn-density-pods scenario. Creates two VMs, one Ngnix server and one client reaching it, on the same UDN per iteration. This scenario is meant to test how many UDNs can be deployed in parallel and how it scales. It requires a version of OCP higher than 4.18, otherwise, UDN feature is not available.

### Virt Capacity Benchmark

Test the capacity of Virtual Machines and Volumes supported by the cluster and a specific storage class.

#### Environment Requirements

In order to verify that the `VirtualMachine` completed their boot and that volume resize propagated successfully, the test uses `virtctl ssh`.
Therefore, `virtctl` must be installed and available in the `PATH`.

See the [Temporary SSH Keys](#temporary-ssh-keys) for details on the SSH keys used for the test

#### Test Sequence

The test runs a workload in a loop without deleting previously created resources. By default it will continue until a failure occurs.
Each loop is comprised of the following steps:
- Create VMs
- Resize the root and data volumes
- Restart the VMs
- Snapshot the VMs
- Migrate the VMs

#### Tested StorageClass

By default, the test will search for the `StorageClass` to use:

1. Use the default `StorageClass` for Virtualization annotated with `storageclass.kubevirt.io/is-default-virt-class`
2. If does not exist, use general default `StorageClass` annotated with `storageclass.kubernetes.io/is-default-class`
3. If does not exist, fail the test before starting

To use a different one, use `--storage-class` to provide a different name.

Please note that regardless to which `StorageClass` is used, it must:
- Support Volume Expansion: `allowVolumeExpansion: true`.
- Have a corresponding `VolumeSnapshotClass` using the same provisioner

#### Test Namespace

All `VirtualMachines` are created in the same namespace.

By default, the namespace is `virt-capacity-benchmark`. Set it by passing `--namespace` (or `-n`)

#### Test Size Parameters

Users may control the workload sizes by passing the following arguments:
- `--max-iterations` - Maximum number of iterations, or 0 (default) for infinite. In any case, the test will stop upon failure
- `--vms` - Number of VMs for each iteration (default 5)
- `--data-volume-count` - Number of data volumes for each VM (default 9)
- `--min-vol-size` - Set the minimal volume size supported by the storage class
- `--min-vol-inc-size` - Set the minimal volume size increment supported by the storage class

#### Temporary SSH Keys

The test generated the SSH keys automatically.
By default, it stores the pair in a temporary directory.
Users may choose the store the key in a specified directory by setting `--ssh-key-path`

#### Skip test parts

Some storage classes have limitations requiring the test to skip some parts:
- `--skip-resize-job` - Skip volume resize job. Use when e.g. `allowVolumeExpansion` is `false`
- `--skip-migration-job` - Skip the migration job. Use when e.g. `RWX` `accessMode` is not supported

### Virt Clone

Test the capacity and performance of starting multiple virtual machines with a root disk as clones of a single volume. This test comes to mimic VDI sequence

#### Test Sequence

The test runs the following sequence:
1. Create a `VirtualMachine` in namespace A
2. Stop the `VirtualMachine`
3. Create a `DataVolume` in namespace B using the rootdisk of the `VirtualMachine` as the source
4. If the `dataImportCronSourceFormat` field of the `StorageProfile` `status` is set to `snapshot`, or `--use-snapshot` is set to `true`, create a `VolumeSnapshot` of the DataVolume
5. Create a `DataSource`, setting the `source` field to either the `VolumeSnapshot` (if was created) or the `DataVolume`
6. Create `VirtualMachine` in namespace B based in the `DataSource`

#### Tested StorageClass

By default, the test will use the default `StorageClass`. To use a different one, use `--storage-class` to provide a different name.

If `--use-snapshot` is explicitly set to `true` a corresponding `VolumeSnapshotClass` using the same provisioner must exist.
Otherwise, the test will check the `StorageProfile` for the `StorageClass` and act accordingly.

#### Test Namespace

The test creates `VirtualMachines` in two namespaces: `<baseName>-base` and `<baseName>-clones`

By default, the `baseName` is `virt-clone`. Set it by passing `--namespace` (or `-n`)

#### Test Size Parameters

Users may control the workload sizes by passing the following arguments:
- `--vms` - Number of `VirtualMachines` to create in step 6

#### Volume Access Mode

By default, volumes are created with `ReadWriteMany` access mode as this is the recommended configuration for `VirtualMachines`.
If not supported, the access mode may be changes by setting `--access-mode`. The supported values are `RO`, `RWO` and `RWX`.

#### Temporary SSH Keys

In order to verify that the VMs actually completed booting, the test generates an SSH key pair.
By default, it stores the pair in a temporary directory.
Users may choose the store the key in a specified directory by setting `--ssh-key-path`

### Virt Ephemeral Restart

Test the performance of restarting ephemeral `VirtalMachine`s. Kubernetes native ephemeral volumes use local node storage. As a result, the cannot be used on large scale deployment.
Instead, a restart is implemented by stopping the `VirtualMachine`, deleting the `DataVolume` backing its root volume and starting it.

#### Test Sequence

The test runs the following sequence:
1. Create a `DataVolume` using a container image as the source
2. If the `dataImportCronSourceFormat` field of the `StorageProfile` `status` is set to `snapshot`, or `--use-snapshot` is set to `true`, create a `VolumeSnapshot` of the DataVolume
3. Create a `DataSource`, setting the `source` field to either the `VolumeSnapshot` (if was created) or the `DataVolume`
4. Create `VirtualMachine`s based in the `DataSource`
5. Stop all `VirtualMachine`s
6. In batches, delete the `DataVolume` backing the root disk and start the `VirtualMachine`s

#### Tested StorageClass

By default, the test will use the default `StorageClass`. To use a different one, use `--storage-class` to provide a different name.

If `--use-snapshot` is explicitly set to `true` a corresponding `VolumeSnapshotClass` using the same provisioner must exist.
Otherwise, the test will check the `StorageProfile` for the `StorageClass` and act accordingly.

#### Test Namespace

All `VirtualMachines` are created in the same namespace.

By default, the namespace is `virt-ephemeral-restart`. Set it by passing `--namespace` (or `-n`)

#### Test Size Parameters

Users may control the workload sizes by passing the following arguments:
- `--iteration-vms` - Number of `VirtualMachines` to batch in each group in step 6
- `--iteration-vms` - Number of batches to run in step 6

!!! Note

    The total number of `VirtualMachines` created is `--iteration-vms` * `--iteration-vms`

#### Volume Access Mode

By default, volumes are created with `ReadWriteMany` access mode as this is the recommended configuration for `VirtualMachines`.
If not supported, the access mode may be changes by setting `--access-mode`. The supported values are `RO`, `RWO` and `RWX`.

#### Temporary SSH Keys

In order to verify that the VMs actually completed booting, the test generates an SSH key pair.
By default, it stores the pair in a temporary directory.
Users may choose the store the key in a specified directory by setting `--ssh-key-path`

## CUDN BGP Workload

This workload tests BGP route exchange import and export scenarios for the CUDNs.

Assumptions in this workload:
1. Kube burner should be running on the host which is not used as a bastion host to deploy OCP cluster.
   a) Routes in the CUDN gateway router when we have the same host as the deployment host and kube burner host
```console
sh-5.1# ovn-nbctl lr-route-list GR_cluster_udn_cudn.0_e34-h14-000-r650.rdu2.scalelab.redhat.com
IPv4 Routes
Route Table <main>:
              20.0.2.0/24             192.168.0.1 dst-ip rtoe-GR_cluster_udn_cudn.0_e34-h14-000-r650.rdu2.scalelab.redhat.com
                0.0.0.0/0               192.168.0.1 dst-ip rtoe-GR_cluster_udn_cudn.0_e34-h14-000-r650.rdu2.scalelab.redhat.com
```
   b) Routes in the CUDN gateway router when the deployment host and the kube burner host is different
```console
sh-5.1# ovn-nbctl lr-route-list GR_cluster_udn_cudn.0_e34-h14-000-r650.rdu2.scalelab.redhat.com
IPv4 Routes
Route Table <main>:
              20.0.2.0/24             192.168.0.145 dst-ip rtoe-GR_cluster_udn_cudn.0_e34-h14-000-r650.rdu2.scalelab.redhat.com
                0.0.0.0/0               192.168.0.1 dst-ip rtoe-GR_cluster_udn_cudn.0_e34-h14-000-r650.rdu2.scalelab.redhat.com
```
   In case of same host i.e scenario a, ping reply will reach the kube burner even if the route "20.0.2.0/24" is not added, but using default route "0.0.0.0/0 192.168.0.1". Our purpose of the testing is to verify if the external route "20.0.2.0/24" is properly added or not in CUDN's gateway router. So we want the ping test to fail if this route is not correctly imported.

2. An external FRR will be running on the same host where the kube burner is running.
   a) Here kube burner is the generator of the external routes. External FRR imports these routes into the OCP cluster through internal FRR and OVN.
   b) Also when external FRR routes receive the routes from the OCP cluster, kube burner validates them.
3. External FRR will be created by the user and configured to pair up with OCP cluster's OVN internal FRR routers
2. External FRR additionally configured to advertise the host routes i.e

```console
vtysh
configure terminal
router bgp 64512
redistribute static
redistribute connected
end
write
```

Unlike a UDN network, a CUDN network will be cluster scoped and can be used by multiple namespaces. However we restrict this to one namespace by default as our aim here is testing BGP route exchange.

This workload defines multiple jobs as per CUDN requirment. Some of the requirements:
1. Namespace with label "k8s.ovn.org/primary-user-defined-network" before the CUDN creation
2. OVN should create all the necessary resources for CUDN before a pod is created on it. Currently we don't have a mechanism to detect if the OVN has created all CUDN's resources. So we are using separate jobs for CUDN and pods with jobPause. Workload defines only one pod per CUDN.
3. RouteAdvertiments CRD selecting the CUDN. We use 1:1 RA:CUDN mapping.

As we want to measure BGP route exchange latency, this workload skips measurements for all the resources except RouteAdvertisements.

This workload has 2 BGP route exchange scenarios 1) route export scenaio 2) route import scenario
When a RouteAdvertisement is created, it advertises the selected CUDN's subnet to outside cluster. However, an external route is imported to the CUDN's gateway router only when it is advertised by the routeAdvertisment. Hence RouteAdvertisemnt for CUDN is mandatory for both export and import scenarios.

Sequent of events during workload execution
1. Job1 creates namespaces
2. Job2 creates CUDNs
3. Job3 creates Pods
4. When Job4 execution starts, Kube burner calls start measurement.
   RouteAdvertisment Latency measurement code then
   a. Maintains a list of CUDN subnets and the pod addresses.
   b. Starts export scenario
      i) Main thread subscribes (through routeCh channel) to kernel's netlink sockets for route monitoring
      ii) Starts export workers, which read from the subscribed routeCh channel
      iii) Registers an informer for notifying router advertisement resource creation events
5. Kube burner creates  RouteAdvertisments for CUDNs
   a. Kubernetes API notify the RouteAdvertisment resource to the listening kube burner
   b. OVN using the internal FRR advertises this route to the outside cluster (i.e to external FRR)
   c. External FRR adds the routes to the host. Kernel notifies the routes to the kube burner using netlink sockets
6. RouteAdvertisment Latency measurement code (watchers and export worker threads) then
   a. Records RouteAdvertisement name and creation timestamp when routeadvertisement resource is detected by the API
   b. For each route notified by the kernel, pings the corresponding CUDN's pod and records ping success timestamp
7. Kube burner calls stop measurements. RouteAdvertisment Latency measurement code then
   a. Waits for export scenario completion
   b. Starts import scenario
      i) Main thread creates interfaces, prepares IP addresses and pods to ping (which are needed for worker threads)
      ii) Starts import workers
      iii) Import workers add IP addresses on the interfaces
      iv) Kernel creates the linux route for the added IP address. Then external FRR router exports this to the cluster.
      v) OVN imports this route into the CUDN's gateway router.
      vi) Import worker pings the CUDN pod using the CUDN pod address as destination address and above added IP address as the source address.
      vii) Import worker records the ping success timestamp
   c. Waits for import scenario complettion
8. kube burner indexes all the latency measurements

### RouteAdvertisement Latency Metrics

RouteAdvetisements latency is calculated for both import and export scenarios, these latency metrics are in ms. It can be enabled with:

```json
  measurements:
  - name: podLatency
```

The metrics collected are route advertisement latency timeseries (raLatencyMeasurement) and six documents holding a summary with different route latency quantiles of ping test and netlink route detection latency (raLatencyQuantilesMeasurement).

One document, such as the following, is indexed per each internal (through Routeadvertisment CRD) and external route (adding ip address on dummy interface) created by the workload:

```json
[
  {
    "timestamp": "2025-04-15T08:41:10Z",
    "metricName": "raLatencyMeasurement",
    "uuid": "2c8a64a8-0409-4d17-8643-c28db8216821",
    "jobName": "udn-bgp-route-advertisements",
    "routeAdvertisementName": "ra-0",
    "metadata": {
      "ocpMajorVersion": "4.19",
      "ocpVersion": "4.19.0-ec.3"
    },
    "scenario": "ExportRoutes",
    "latency": [
      10031
    ],
    "minReadyLatency": 10031,
    "maxReadyLatency": 10031,
    "readyLatency": 10031,
    "netlinkRouteLatency": [
      10026
    ],
    "maxNetlinkRouteLatency": 10026,
    "minNetlinkRouteLatency": 10026,
    "p99NetlinkRouteLatency": 10026
  },
  {
    "timestamp": "2025-04-15T08:42:20.060393739Z",
    "metricName": "raLatencyMeasurement",
    "uuid": "2c8a64a8-0409-4d17-8643-c28db8216821",
    "jobName": "udn-bgp-route-advertisements",
    "routeAdvertisementName": "20.0.1.1/24",
    "metadata": {
      "ocpMajorVersion": "4.19",
      "ocpVersion": "4.19.0-ec.3"
    },
    "scenario": "ImportRoutes",
    "latency": [
      13
    ],
    "minReadyLatency": 13,
    "maxReadyLatency": 13,
    "readyLatency": 13
  }
]
```

## Custom Workload: Bring your own workload

To kickstart kube-burner-ocp with a custom workload, `init` becomes your go-to command. This command is equipped with flags that enable to seamlessly integrate and run your personalized workloads. Here's a breakdown of the flags accepted by the init command:

```console
$ kube-burner-ocp init --help
Runs custom workload

Usage:
  kube-burner-ocp init [flags]

Flags:
    --churn                            Enable churning (default true)
    --churn-cycles int                 Churn cycles to execute
    --churn-delay duration             Time to wait between each churn (default 2m0s)
    --churn-deletion-strategy string   Churn deletion strategy to use (default "default")
    --churn-duration duration          Churn duration (default 5m0s)
    --churn-percent int                Percentage of job iterations that kube-burner will churn each round (default 10)
    -c, --config string                    Config file path or url
    -h, --help                             help for init
    --iterations int                   Job iterations. Mutually exclusive with '--pods-per-node' (default 1)
    --iterations-per-namespace int     Iterations per namespace (default 1)
    --namespaced-iterations            Namespaced iterations (default true)
    --pods-per-node int                Pods per node. Mutually exclusive with '--iterations' (default 50)
    --service-latency                  Enable service latency measurement
```

Creating a custom workload for kube-burner-ocp is a seamless process, and you have the flexibility to craft it according to your specific needs. Below is a template to guide you through the customization of your workload:

```yaml
---
indexers:
  - esServers: ["{{.ES_SERVER}}"]
    insecureSkipVerify: true
    defaultIndex: {{.ES_INDEX}}
    type: opensearch
global:
  gc: {{.GC}}
  gcMetrics: {{.GC_METRICS}}
  measurements:
    - name: <metric_name>
      thresholds:
        - <threshold_key>: <threshold_value>

jobs:
  - name: <job_name>
    namespace: <namespace_name>
    jobIterations: <number of iterations>
    qps: {{.QPS}}     # Both QPS and BURST can be specified through the CLI
    burst: {{.BURST}}
    namespacedIterations: <bool>
    podWait: <bool>
    waitWhenFinished: <bool>
    preLoadImages: <bool>
    preLoadPeriod: <preLoadPeriod_in_seconds>
    namespaceLabels:
      <namespaceLabels_key>: <namespaceLabels_value>
    objects:

      - objectTemplate: <template_config>
        replicas: <replica_int>
        inputVars:
          <inputVar1>:<inputVar1_value>
```

You can start from scratch or explore pre-built workloads in the /config folder, offering a variety of examples used by kube-burner-ocp. Dive into the details of each section in the template to tailor the workload precisely to your requirements. Experiment, iterate, and discover the optimal configuration for your workload to seamlessly integrate with kube-burner-ocp.

## Index

Just like the regular kube-burner, `kube-burner-ocp` also has an indexing functionality which is exposed as `index` subcommand.

```console
$ kube-burner-ocp index --help
If no other indexer is specified, local indexer is used by default

Usage:
  kube-burner-ocp index [flags]

Flags:
  -m, --metrics-profile string     Metrics profile file (default "metrics.yml")
      --metrics-directory string   Directory to dump the metrics files in, when using default local indexing (default "collected-metrics")
  -s, --step duration              Prometheus step size (default 30s)
      --start int                  Epoch start time
      --end int                    Epoch end time
  -j, --job-name string            Indexing job name (default "kube-burner-ocp-indexing")
      --user-metadata string       User provided metadata file, in YAML format
  -h, --help                       help for index
```

## Metrics-profile type

By specifying `--profile-type`, kube-burner can use two different metrics profiles when scraping metrics from prometheus. By default is configured with `both`, meaning that it will use the regular metrics profiles bound to the workload in question and the reporting metrics profile.

When using the regular profiles ([metrics-aggregated](https://github.com/kube-burner/kube-burner-ocp/blob/master/cmd/config/metrics-aggregated.yml) or [metrics](https://github.com/kube-burner/kube-burner-ocp/blob/master/cmd/config/metrics.yml)), kube-burner scrapes and indexes metrics timeseries.

The reporting profile is very useful to reduce the number of documents sent to the configured indexer. Thanks to the combination of aggregations and instant queries for prometheus metrics, and 4 summaries for latency measurements, only a few documents will be indexed per benchmark. This flag makes possible to specify one or both of these profiles indistinctly.

## Customizing workloads

It is possible to customize any of the above workload configurations by extracting, updating, and finally running it:

```console
$ kube-burner-ocp node-density --extract
$ ls
alerts.yml  metrics.yml  node-density.yml  pod.yml  metrics-report.yml
$ vi node-density.yml                               # Perform modifications accordingly
$ kube-burner-ocp node-density --pods-per-node=100  # Run workload
```
