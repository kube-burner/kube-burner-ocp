# OpenShift Wrapper

This plugin is a very opinionated OpenShift wrapper designed to simplify the execution of different workloads in this Kubernetes distribution.

Executed with `kube-burner-ocp`, it looks like:

```console
$ kube-burner-ocp help
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
  version                        Print the version number of kube-burner
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
- Adds OpenShift metadata to the generated documents.
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

## Network Policy workloads

With the help of [networkpolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/) object we can control traffic flow at the IP address or port level in Kubernetes. A networkpolicy can come in various shapes and sizes. Allow traffic from a specific namespace, Deny traffic from a specific pod IP, Deny all traffic, etc. Hence we have come up with a few test cases which try to cover most of them. They are as follows.

### networkpolicy-multitenant

- 500 namespaces
- 20 pods in each namespace. Each pod acts as a server and a client
- Default deny networkpolicy is applied first that blocks traffic to any test namespace
- 3 network policies in each namespace that allows traffic from the same namespace and two other namespaces using namespace selectors

### networkpolicy-matchlabels

- 5 namespaces
- 100 pods in each namespace. Each pod acts as a server and a client
- Each pod with 2 labels and each label shared is by 5 pods
- Default deny networkpolicy is applied first
- Then for each unique label in a namespace we have a networkpolicy with that label as a podSelector which allows traffic from pods with some other randomly selected label. This translates to 40 networkpolicies/namespace

### networkpolicy-matchexpressions

- 5 namespaces
- 25 pods in each namespace. Each pod acts as a server and a client
- Each pod with 2 labels and each label shared is by 5 pods
- Default deny networkpolicy is applied first
- Then for each unique label in a namespace we have a networkpolicy with that label as a podSelector which allows traffic from pods which *don't* have some other randomly-selected label. This translates to 10 networkpolicies/namespace

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

## Workers Scale 
As a day2 operation, we can use this option to scale our cluster's worker nodes to a desired count and capture their bootup times.

!!! Note    

    This is only supported for openshift clusters hosted on AWS at the moment.

### Options
```
$ kube-burner-ocp workers-scale

Usage:
  kube-burner-ocp workers-scale [flags]

Flags:
  -m, --metrics-profile string        Comma-separated list of metric profiles (default "metrics.yml")
      --metrics-directory string      Directory to dump the metrics files in, when using default local indexing (default "collected-metrics")
      --step duration                 Prometheus step size (default 30s)
      --additional-worker-nodes int   Additional workers to scale (default 3)
      --enable-autoscaler             Enables autoscaler while scaling the cluster
      --scale-event-epoch int         Scale event epoch time
      --user-metadata string          User provided metadata file, in YAML format
      --tarball-name string           Dump collected metrics into a tarball with the given name, requires local indexing
  -h, --help                          help for workers-scale

```

### Examples
1. Manually scale a cluster to desired node count and capture bootup times.
```
$ kube-burner-ocp workers-scale --additional-worker-nodes 24
```
2. Auto scale a cluster to a desired node count and capture bootup times. Also disable garbage collection.
```
$ kube-burner-ocp workers-scale --additional-worker-nodes 24 --enable-autoscaler --gc=false
```
3. Without any scaling, simply capture bootup times on an already scaled cluster. We just have to specify the timestamp when the scale event was triggered.
```
$ kube-burner-ocp workers-scale --scale-event-epoch 1725635502
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
