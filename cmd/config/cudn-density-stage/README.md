# cudn-density Workload

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
  - [CUDN Grouping Model](#cudn-grouping-model)
  - [Network Topology](#network-topology)
  - [Per-Namespace Objects](#per-namespace-objects)
  - [Communication Model](#communication-model)
  - [Cross-Namespace Traffic Pattern](#cross-namespace-traffic-pattern)
  - [OVN-K Load Profile](#ovn-k-load-profile)
- [Job Pipeline](#job-pipeline)
  - [CUDN Settling Pause](#cudn-settling-pause)
  - [Why the CUDN Cleanup Step?](#why-the-cudn-cleanup-step)
- [Measurements](#measurements)
  - [Pod Latency](#pod-latency)
  - [CUDN Latency](#cudn-latency-job-2)
  - [Metrics Profiles](#metrics-profiles)
  - [pprof Collection](#pprof-collection)
- [Usage](#usage)
  - [Basic Run](#basic-run)
  - [Layer 3 with Local Indexing](#layer-3-with-local-indexing)
  - [With Churn](#with-churn)
  - [With pprof and OpenSearch Indexing](#with-pprof-and-opensearch-indexing)
- [CLI Flags](#cli-flags)
- [Scale Considerations](#scale-considerations)
  - [Scaling Knobs](#scaling-knobs)
- [Cleanup](#cleanup)
- [File Inventory](#file-inventory)

---

## Overview

`cudn-density` is a kube-burner-ocp workload that stress-tests **OVN-Kubernetes** networking at scale using **ClusterUserDefinedNetworks (CUDNs)**. It simulates a multi-tenant environment where groups of namespaces share a primary CUDN and run a 3-tier microservice application with cross-namespace communication, network policies, egress firewalls, and resource quotas.

The workload measures:

- **CUDN creation latency** — how fast OVN-K can provision a new shared network
- **Cross-namespace pod readiness** — how fast pods can communicate across namespace boundaries on a CUDN primary network
- **OVN-K control plane resource consumption** — CPU, memory, and flow programming metrics under load

---

## Architecture

### CUDN Grouping Model

Namespaces are organized into **CUDN groups**. Each group shares a single ClusterUserDefinedNetwork as its primary network. By default, each group contains 5 namespaces (configurable via [`--namespaces-per-cudn`](#cli-flags)). Traffic flows only within each group — cross-group traffic is blocked by [NetworkPolicies](#3-tier-communication-model).

```
CUDN-0 (primary network)          CUDN-1 (primary network)
├── cudn-density-0                 ├── cudn-density-5
├── cudn-density-1                 ├── cudn-density-6
├── cudn-density-2                 ├── cudn-density-7
├── cudn-density-3                 ├── cudn-density-8
└── cudn-density-4                 └── cudn-density-9
```

With `--iterations=10 --namespaces-per-cudn=5`, 2 CUDNs are created, each spanning 5 namespaces. The `--iterations` value must be divisible by `--namespaces-per-cudn`.

### Network Topology

Each CUDN can use either **Layer 2** (default) or **Layer 3** (`--layer3`) topology:

| Topology | Subnet | Routing | Use Case |
|----------|--------|---------|----------|
| **Layer 2** | Shared `10.132.0.0/16` per CUDN | Flat L2, direct pod-to-pod | Default, simpler |
| **Layer 3** | Unique `/16` per CUDN (e.g., `40.0.0.0/16`) | Per-node `/24` host subnets, routed | Production-like, scalable |

### Per-Namespace Objects

Each namespace contains the following objects:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Namespace: cudn-density-N                     │
│                                                                  │
│  Deployments (4 pods total)          Services (3)                │
│  ┌─────────────────────┐             ┌──────────────────────┐    │
│  │ server-1 (2 pods)   │◄────────────│ cudn-svc (8080/8443) │    │
│  │ app-1    (1 pod)    │             │ server-1-svc         │    │
│  │ client-1 (1 pod)    │             │ cudn-app-headless    │    │
│  └─────────────────────┘             └──────────────────────┘    │
│                                                                  │
│  NetworkPolicies (5)                 Other                       │
│  ┌─────────────────────┐             ┌──────────────────────┐    │
│  │ deny-all            │             │ EgressFirewall (9    │    │
│  │ allow-cudn-ingress  │             │   rules)             │    │
│  │ allow-cudn-egress   │             │ ResourceQuota        │    │
│  │ allow-app-ingress   │             │ LimitRange           │    │
│  │ allow-app-egress    │             │ ConfigMap            │    │
│  └─────────────────────┘             └──────────────────────┘    │
└─────────────────────────────────────────────────────────────────┘
```

### Communication Model

The workload deploys a 3-tier structure (client, app, server) but only the **client → server** path carries actual traffic. The app tier exists so that its network policies match real pods, generating OVN-K ACL and address set load.

```
  Actual traffic:
  ───────────────

   ┌──────────┐                           ┌──────────┐         ┌──────────┐
   │  CLIENT  │──── HTTP :8080 ──────────►│  SERVER  │         │   APP    │
   │  (curl)  │     via cudn-svc          │  (nginx) │         │(sampleapp│
   └──────────┘                           │  :8080   │         │  :8080   │
     1 pod/ns                             │  :8443   │         │          │
                                          └──────────┘         └──────────┘
                                           2 pods/ns            1 pod/ns
```

**NetworkPolicy enforcement:**

| Policy | Selector | Allows | Exercised? |
|--------|----------|--------|------------|
| `deny-all` | all pods | Nothing (baseline) | Yes — blocks cross-group traffic |
| `allow-cudn-ingress` | `app=nginx` | Ingress from `client` and `sampleapp` within CUDN group | **Yes** — client curls nginx |
| `allow-cudn-egress` | `app=client` | Egress to `nginx` and `sampleapp` within CUDN group + DNS | **Yes** — client curls + DNS |
| `allow-app-ingress` | `app=sampleapp` | Ingress from `client` within CUDN group | Selectors match real pods — OVN-K ACL load |
| `allow-app-egress` | `app=sampleapp` | Egress to `nginx` within CUDN group + DNS | Selectors match real pods — OVN-K ACL load |

> **Note:** NetworkPolicies use **named ports** (`http`, `https`) instead of numeric ports, forcing OVN-K to resolve port names against pod specs — a more expensive code path that generates more ACL computation.

### Cross-Namespace Traffic Pattern

Each client generates continuous HTTP traffic to peer namespaces within its CUDN group:

```
CUDN-0 group (namespaces 0-4):

  Continuous traffic (every 10s):             Readiness probes (every 1s):
  ─────────────────────────                   ────────────────────────────
  client in ns-0 → curls ns-1, ns-2, ns-3, ns-4    probes ns-1
  client in ns-1 → curls ns-2, ns-3, ns-4          probes ns-2
  client in ns-2 → curls ns-3, ns-4                probes ns-3
  client in ns-3 → curls ns-4                      probes ns-4
  client in ns-4 → sleeps (last in group)           probes ns-0 (wraps)
```

**Key design choices:**

- **Readiness probes target peer namespaces** — the client pod only becomes `Ready` when cross-namespace CUDN networking is fully plumbed. The `Ready` latency directly measures end-to-end CUDN network readiness, not just local container startup.
- **Directed pipeline** — each client curls namespaces ahead of it, avoiding redundant bidirectional traffic.
- **Wrap-around readiness** — the last namespace in each group probes the first, forming a ring that validates the entire CUDN group.

### OVN-K Load Profile

| OVN-K Component | Load Source |
|-----------------|------------|
| **Logical switch ports** | 4 pods/ns |
| **OVN load balancers** | 3 services/ns, each with dual ports (8080+8443) |
| **ACLs** | 5 NetworkPolicies/ns with cross-namespace selectors and named ports |
| **Address sets** | Each NP with `namespaceSelector` creates an address set spanning the CUDN group |
| **EgressFirewall rules** | 8 rules/ns (CIDR + DNS-based) |
| **NADs** | 1 per namespace, managed by the CUDN controller |
| **Network plumbing** | CNI plugin creates OVS ports + flows for each pod on the primary CUDN |
| **Endpoint tracking** | EndpointSlices for 3 services, headless service resolves to individual pod IPs |

---

## Job Pipeline

```
Job 1          Job 2              Job 3              Job 4         Job 5
Create         Create CUDNs +     Deploy Infra +     Cleanup       Cleanup
Namespaces     Settling Pause     Workload           Namespaces    CUDNs
───────── ──►  ────────────── ──►  ────────────── ──►  ────────── ► ──────
 N ns          N/G CUDNs          Services, NPs      (GC only)    (GC only)
 + configmap   wait for           EgressFW, Quotas   no-wait       wait
               NetworkCreated     + Deployments
               measure latency    2m metrics pause
               + settling pause
```

| # | Job Name | Type | What It Does |
|---|----------|------|-------------|
| 1 | `cudn-density-create-namespaces` | create | Creates N namespaces with UDN labels + a configmap per namespace |
| 2 | `cudn-density-create-cudn-l2/l3` | create | Creates N/group_size CUDNs, waits for `NetworkCreated=True`. [Measures CUDN latency](#cudn-latency-job-2). Then pauses for [`--job-pause`](#cudn-settling-pause) to allow OVN-K to settle |
| 3 | `cudn-density-workload` | create | Deploys infra (services, NPs, EgressFirewall, ResourceQuota, LimitRange) and workload (server, app, client deployments) in a single job. Infra objects have `churn: false`. Pauses 2m after deployment for [metrics collection](#metrics-profiles) |
| 4 | `cudn-density-cleanup-namespaces` | delete | [Deletes namespaces](#why-the-cudn-cleanup-step) without waiting (only when `--gc=true`). Pods are killed, NADs start terminating |
| 5 | `cudn-density-cleanup-cudns` | delete | [Deletes CUDNs](#why-the-cudn-cleanup-step), releasing NAD finalizers so namespaces finish terminating (only when `--gc=true`) |

### CUDN Settling Pause

After CUDN creation (Job 2), a configurable pause (`--job-pause`, default 30m) allows OVN-K to finish compiling the CUDN logical topology in the NB/SB databases and programming OVS flows on all nodes before workload pods are deployed. This is particularly important for Layer 2 topologies with Interconnect enabled, where the OVN NB DB client becomes a bottleneck when processing CUDN topology changes and pod port bindings simultaneously.

### Why the CUDN Cleanup Step?

CUDNs have a finalizer (`k8s.ovn.org/user-defined-network-protection`) that prevents deletion while NADs exist. NADs live inside namespaces and have their own finalizers managed by the CUDN controller. This creates a dependency chain:

```
Namespace deletion blocked by → NAD finalizer blocked by → CUDN existence
```

Job 4 deletes namespaces without waiting (`waitForDeletion: false`), then Job 5 deletes CUDNs (`waitForDeletion: true`), releasing the finalizers so namespaces finish terminating. See [Cleanup](#cleanup) for manual cleanup instructions.

---

## Measurements

### Pod Latency

Standard kube-burner pod latency measurement tracking `PodScheduled`, `Initialized`, `ContainersReady`, and `Ready` conditions. Only indexed for Jobs 2 and 3:

- **Job 2**: Empty (CUDNs are cluster-scoped, no pods)
- **Job 3**: The meaningful measurement — includes OVN network plumbing time + cross-namespace readiness probe validation

> **Note:** The `Ready` latency in Job 3 is higher than typical workloads because the readiness probe validates **cross-namespace** connectivity over the CUDN, not just local container readiness. This is by design — it directly measures CUDN network plumbing time.

### CUDN Latency (Job 2)

Custom measurement (`cudnLatency`) that tracks how long each CUDN takes from creation to `NetworkCreated=True`. Uses the condition's `lastTransitionTime` for accurate measurement rather than wall-clock time. Results are indexed as `cudnLatencyMeasurement` documents with `NetworkCreatedLatency` in milliseconds.

### Metrics Profiles

Two metrics profiles are collected by default:

- **`metrics.yml`**: Standard OpenShift/kube metrics
- **`metrics-cudn.yml`**: OVN-K specific metrics including:
  - ovnkube-controller CPU/memory per pod per node
  - ovnkube-cluster-manager CPU/memory
  - ovs-vswitchd CPU/memory
  - CRI-O network setup/teardown latency P99
  - NetworkPolicy count
  - OVN Northbound DB tx/rx bytes
  - OpenFlow rule count per bridge

### pprof Collection

With [`--pprof`](#cli-flags), CPU and heap profiles are collected from ovnkube-controller and ovnkube-control-plane at configurable intervals (`--pprof-interval`).

---

## Usage

### Basic Run

```bash
kube-burner-ocp cudn-density --iterations=50
```

### Layer 3 with Local Indexing

```bash
kube-burner-ocp cudn-density \
  --iterations=100 \
  --layer3 \
  --namespaces-per-cudn=10 \
  --local-indexing
```

### With Churn

> **Important:** Only `--churn-mode=objects` is supported. Namespace churn is not supported because [CUDN finalizers block namespace deletion](#why-the-cudn-cleanup-step).

```bash
kube-burner-ocp cudn-density \
  --iterations=50 \
  --churn-duration=30m \
  --churn-percent=20 \
  --churn-delay=1m
```

### With pprof and OpenSearch Indexing

```bash
kube-burner-ocp cudn-density \
  --iterations=100 \
  --pprof \
  --pprof-interval=5m \
  --es-server=https://opensearch.example.com \
  --es-index=cudn-density
```

---

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--iterations` | *(required)* | Total number of namespaces to create |
| `--namespaces-per-cudn` | `5` | Namespaces per CUDN group. `iterations` must be divisible by this value |
| `--layer3` | `false` | Use Layer3 topology (default is Layer2). See [Network Topology](#network-topology) |
| `--job-pause` | `1m` | Pause after CUDN creation to allow OVN-K network settling before workload deployment. See [CUDN Settling Pause](#cudn-settling-pause) |
| `--pod-ready-threshold` | `2m` | P99 pod ready timeout threshold |
| `--pprof` | `false` | Enable [pprof collection](#pprof-collection) for ovnkube components |
| `--pprof-interval` | `0` | Interval between pprof collections |
| `--churn-cycles` | `0` | Number of churn cycles to execute |
| `--churn-duration` | `0` | Total churn duration |
| `--churn-delay` | `2m` | Delay between churn rounds |
| `--churn-percent` | `10` | Percentage of iterations churned per round |
| `--churn-mode` | `objects` | Churn mode (`objects` only; `namespaces` [not supported](#why-the-cudn-cleanup-step)) |
| `--metrics-profile` | `metrics.yml,metrics-cudn.yml` | Comma-separated list of [metrics profiles](#metrics-profiles) to use |
| `--gc` | `true` | Garbage collect created resources on completion. See [Cleanup](#cleanup) |

---

## Scale Considerations

With default `--namespaces-per-cudn=5`:

| Iterations | CUDNs | Namespaces | Pods/ns | Total Pods | Services/ns | NPs/ns | OVN LB entries |
|-----------|-------|------------|---------|------------|-------------|--------|----------------|
| 10 | 2 | 10 | 4 | 40 | 3 | 5 | 60 |
| 50 | 10 | 50 | 4 | 200 | 3 | 5 | 300 |
| 100 | 20 | 100 | 4 | 400 | 3 | 5 | 600 |
| 500 | 100 | 500 | 4 | 2,000 | 3 | 5 | 3,000 |

OVN LB entries = services/ns x 2 ports x namespaces.

### Scaling Knobs

| Knob | Impact | Description |
|------|--------|-------------|
| `--namespaces-per-cudn` | **High** | Most impactful for NP/address-set load. Each NP with cross-namespace selectors creates address sets spanning all namespaces in the CUDN group. Going from 5 → 20 quadruples the address set size |
| `--iterations` | **High** | Controls total namespace count. More namespaces = more pods, services, NPs, and OVN logical ports |
| `--layer3` | **Low** | L3 uses per-node subnets and routing, slightly more OVN-K work than flat L2 |

---

## Cleanup

Due to CUDN finalizers, manual cleanup requires a specific order:

```bash
# 1. Delete namespaces first (removes pods, then NADs start terminating)
oc delete ns -l kube-burner.io/uuid --wait=false

# 2. Delete CUDNs (releases NAD finalizers, allowing namespaces to finish terminating)
oc delete clusteruserdefinednetworks --all
```

> **Note:** With `--gc=true`, this is handled automatically by [Job 4](#job-pipeline).

---

## File Inventory

### Job Configuration

| File | Description |
|------|-------------|
| [`cudn-density.yml`](cudn-density.yml) | Main job configuration with 5-job pipeline |

### Network Templates

| File | Description |
|------|-------------|
| [`cudn-l2.yml`](cudn-l2.yml) | ClusterUserDefinedNetwork template (Layer2 topology) |
| [`cudn-l3.yml`](cudn-l3.yml) | ClusterUserDefinedNetwork template (Layer3 topology) |

### Workload Templates

| File | Description |
|------|-------------|
| [`deployment-server.yml`](deployment-server.yml) | nginx server deployment (named ports: `http`/`https`) |
| [`deployment-app.yml`](deployment-app.yml) | sampleapp middleware deployment |
| [`deployment-client.yml`](deployment-client.yml) | curl client with cross-namespace traffic + [readiness probes](#cross-namespace-traffic-pattern) |
| [`configmap.yml`](configmap.yml) | Configmap to trigger namespace creation |

### Service Templates

| File | Description |
|------|-------------|
| [`service.yml`](service.yml) | ClusterIP service for nginx (dual port: 8080+8443) |
| [`service-headless.yml`](service-headless.yml) | Headless service for sampleapp |
| [`service-server.yml`](service-server.yml) | Per-server-deployment ClusterIP service (dual port) |

### NetworkPolicy Templates

| File | Description |
|------|-------------|
| [`np-deny-all.yml`](np-deny-all.yml) | Default deny-all NetworkPolicy (ingress + egress) |
| [`np-allow-cudn-ingress.yml`](np-allow-cudn-ingress.yml) | Ingress to nginx from client/app (named ports, CUDN group scoped) |
| [`np-allow-cudn-egress.yml`](np-allow-cudn-egress.yml) | Egress from client to nginx/app (named ports, CUDN group scoped) |
| [`np-allow-app-ingress.yml`](np-allow-app-ingress.yml) | Ingress to sampleapp from client (CUDN group scoped) |
| [`np-allow-app-egress.yml`](np-allow-app-egress.yml) | Egress from sampleapp to nginx (named ports, CUDN group scoped) |

### Infrastructure Templates

| File | Description |
|------|-------------|
| [`egressfirewall.yml`](egressfirewall.yml) | OVN EgressFirewall (8 rules: RFC1918, DNS, registries, monitoring) |
| [`resourcequota.yml`](resourcequota.yml) | ResourceQuota per namespace |
| [`limitrange.yml`](limitrange.yml) | LimitRange per namespace |
