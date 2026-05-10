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
- [CUDN Churn](#cudn-churn)
  - [How It Works](#how-it-works)
  - [Execution Flow](#execution-flow)
  - [CUDN Selection](#cudn-selection)
  - [Metrics During CUDN Churn](#metrics-during-cudn-churn)
  - [Cleanup with CUDN Churn](#cleanup-with-cudn-churn)
- [Measurements](#measurements)
  - [Pod Latency](#pod-latency)
  - [CUDN Latency](#cudn-latency-job-2)
  - [Metrics Profiles](#metrics-profiles)
  - [pprof Collection](#pprof-collection)
- [Usage](#usage)
  - [Basic Run](#basic-run)
  - [Layer 3 with Local Indexing](#layer-3-with-local-indexing)
  - [With Pod Churn](#with-pod-churn)
  - [With CUDN Churn](#with-cudn-churn)
  - [Pod Churn + CUDN Churn Combined](#pod-churn--cudn-churn-combined)
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
- **CUDN churn resilience** — how OVN-K handles CUDN lifecycle events (deletion + recreation) while other CUDNs remain active

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

### Normal Mode (no CUDN churn)

```
Job 1          Job 2              Job 3              Job 4         Job 5
Create         Create CUDNs +     Deploy Infra +     Cleanup       Cleanup
Namespaces     Settling Pause     Workload + Churn   Namespaces    CUDNs
───────── ──►  ────────────── ──►  ────────────── ──►  ────────── ► ──────
 N ns          N/G CUDNs          Services, NPs      (GC only)    (GC only)
 + configmap   wait for           EgressFW, Quotas   no-wait       wait
               NetworkAlloc       + Deployments
               measure latency    + pod object churn
               + settling pause   2m metrics pause
```

| # | Job Name | Type | What It Does |
|---|----------|------|-------------|
| 1 | `cudn-density-create-namespaces` | create | Creates N namespaces with UDN labels + a configmap per namespace |
| 2 | `cudn-density-create-cudn-l2/l3` | create | Creates N/group_size CUDNs, waits for `NetworkAllocationSucceeded=True`. [Measures CUDN latency](#cudn-latency-job-2). Then pauses for [`--job-pause`](#cudn-settling-pause) to allow OVN-K to settle |
| 3 | `cudn-density-workload` | create | Deploys infra (services, NPs, EgressFirewall, ResourceQuota, LimitRange) and workload (server, app, client deployments) in a single job. Infra objects have `churn: false`. Pauses 2m after deployment for [metrics collection](#metrics-profiles) |
| 4 | `cudn-density-cleanup-namespaces` | delete | [Deletes namespaces](#why-the-cudn-cleanup-step) without waiting (only when `--gc=true`). Pods are killed, NADs start terminating |
| 5 | `cudn-density-cleanup-cudns` | delete | [Deletes CUDNs](#why-the-cudn-cleanup-step), releasing NAD finalizers so namespaces finish terminating (only when `--gc=true`) |

### CUDN Settling Pause

After CUDN creation (Job 2), a configurable pause (`--job-pause`, default 1m) allows OVN-K to finish compiling the CUDN logical topology in the NB/SB databases and programming OVS flows on all nodes before workload pods are deployed. This is particularly important for Layer 2 topologies with Interconnect enabled, where the OVN NB DB client becomes a bottleneck when processing CUDN topology changes and pod port bindings simultaneously.

### Why the CUDN Cleanup Step?

CUDNs have a finalizer (`k8s.ovn.org/user-defined-network-protection`) that prevents deletion while NADs exist. NADs live inside namespaces and have their own finalizers managed by the CUDN controller. This creates a dependency chain:

```
Namespace deletion blocked by → NAD finalizer blocked by → CUDN existence
```

Job 4 deletes namespaces without waiting (`waitForDeletion: false`), then Job 5 deletes CUDNs (`waitForDeletion: true`), releasing the finalizers so namespaces finish terminating. See [Cleanup](#cleanup) for manual cleanup instructions.

---

## CUDN Churn

CUDN churn tests OVN-K's ability to handle CUDN lifecycle events — deleting and recreating CUDNs along with their dependent namespaces and workloads — while other CUDNs remain active on the cluster.

### How It Works

CUDN churn cannot use kube-burner's built-in `churnConfig` because CUDN deletion requires a specific multi-step sequence (delete namespaces first, then CUDNs) due to NAD finalizer dependencies. Instead, CUDN churn is implemented as a hybrid:

- **Deletion** is handled by Go code that orchestrates the correct finalizer-aware sequence
- **Recreation** is handled by kube-burner YAML jobs using the `iterationStart` feature to create resources at the correct indices

This approach gives full kube-burner measurement and metrics collection (podLatency, cudnLatency, Prometheus scraping) during the recreation phase.

### Execution Flow

When `--cudn-churn-cycles > 0`, the workload runs in three phases:

```
Phase 1: wh.Run("cudn-density.yml")
  ├── Job 1: Create namespaces
  ├── Job 2: Create CUDNs + settling pause
  └── Job 3: Deploy workload + pod churn (if enabled)
      (cleanup jobs skipped — GC disabled)

Phase 2: For each CUDN churn cycle:
  ├── Go code: Delete namespaces → Delete CUDNs → Wait for termination
  └── wh.Run("cudn-density.yml") with CUDN_CHURN_RECREATE=true:
      ├── cudn-churn-create-ns:    Recreate namespaces (iterationStart offset)
      ├── cudn-churn-create-cudns: Recreate CUDNs (iterationStart offset)
      │   └── cudnLatency measurement + settling pause
      └── cudn-churn-workload:     Redeploy workload (iterationStart offset)
          └── podLatency measurement + Prometheus scraping

Phase 3: wh.Run("cudn-density.yml") with CLEANUP_ONLY=true
  ├── Job 4: Delete all namespaces (original + churn-recreated)
  └── Job 5: Delete all CUDNs (original + churn-recreated)
```

### CUDN Selection

CUDNs are selected **deterministically** — always the last N% of CUDNs. The same CUDNs are churned every cycle.

```
Example: 500 namespaces, 5 ns/cudn = 100 CUDNs, 10% churn

numToChurn  = ceil(0.10 * 100) = 10
churnStart  = 100 - 10 = 90

Churned: cudn-90..cudn-99 (namespaces 450-499, 200 pods)
Stable:  cudn-0..cudn-89  (namespaces 0-449, 1800 pods)
```

### Metrics During CUDN Churn

Each churn cycle's recreation phase runs through kube-burner's job framework, so all standard measurements are collected:

| Job | Measurement | What's Captured |
|---|---|---|
| `cudn-churn-create-cudns` | cudnLatency | Time from CUDN creation to `NetworkAllocationSucceeded=True` |
| `cudn-churn-workload` | podLatency | Pod scheduling, initialization, containers ready, pod ready |
| All churn jobs | Prometheus | containerCPU, containerMemory, API rates, node metrics, etc. |

The pod latency watcher uses a job-scoped label selector (`kube-burner.io/job=cudn-churn-workload`), so only pods created during the churn cycle are measured — no double-counting with Phase 1 pods.

### Cleanup with CUDN Churn

The Phase 3 cleanup jobs use dual label selectors to find both original and churn-recreated resources:

- Namespaces: `kube-burner.io/job=cudn-density-create-namespaces` + `kube-burner.io/job=cudn-churn-create-ns`
- CUDNs: `kube-burner.io/job=cudn-density-create-cudn-l2` + `kube-burner.io/job=cudn-churn-create-cudns`

---

## Measurements

### Pod Latency

Standard kube-burner pod latency measurement tracking `PodScheduled`, `Initialized`, `ContainersReady`, and `Ready` conditions. Indexed for:

- **Job 3 (`cudn-density-workload`)**: Initial deployment + pod churn pods
- **CUDN churn (`cudn-churn-workload`)**: Pods redeployed after CUDN recreation (when CUDN churn is enabled)

> **Note:** The `Ready` latency is higher than typical workloads because the readiness probe validates **cross-namespace** connectivity over the CUDN, not just local container readiness. This is by design — it directly measures CUDN network plumbing time.

### CUDN Latency (Job 2)

Custom measurement (`cudnLatency`) that tracks how long each CUDN takes from creation to `NetworkAllocationSucceeded=True`. Uses the condition's `lastTransitionTime` for accurate measurement rather than wall-clock time. Results are indexed as `cudnLatencyMeasurement` documents. Collected during both initial CUDN creation (Job 2) and CUDN churn recreation (`cudn-churn-create-cudns`).

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

### With Pod Churn

> **Important:** Only `--churn-mode=objects` is supported. Namespace churn is not supported because [CUDN finalizers block namespace deletion](#why-the-cudn-cleanup-step).

```bash
kube-burner-ocp cudn-density \
  --iterations=50 \
  --churn-cycles=5 \
  --churn-percent=50 \
  --churn-delay=2m
```

### With CUDN Churn

```bash
kube-burner-ocp cudn-density \
  --iterations=50 \
  --namespaces-per-cudn=5 \
  --cudn-churn-cycles=3 \
  --cudn-churn-percent=10
```

This churns 1 CUDN per cycle (10% of 10 CUDNs), deleting and recreating 5 namespaces and 20 pods each cycle.

### Pod Churn + CUDN Churn Combined

Pod churn and CUDN churn run as sequential phases — pod churn happens within Job 3, then CUDN churn runs after.

```bash
kube-burner-ocp cudn-density \
  --iterations=500 \
  --namespaces-per-cudn=5 \
  --churn-cycles=2 --churn-percent=50 \
  --cudn-churn-cycles=2 --cudn-churn-percent=10 \
  --job-pause=5m
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
| `--pod-ready-threshold` | `0` | P99 pod ready timeout threshold |
| `--pprof` | `false` | Enable [pprof collection](#pprof-collection) for ovnkube components |
| `--pprof-interval` | `0` | Interval between pprof collections |
| `--churn-cycles` | `0` | Number of pod churn cycles to execute |
| `--churn-duration` | `0` | Total pod churn duration |
| `--churn-delay` | `2m` | Delay between pod churn rounds |
| `--churn-percent` | `10` | Percentage of deployments churned per round |
| `--churn-mode` | `objects` | Churn mode (`objects` only; `namespaces` [not supported](#why-the-cudn-cleanup-step)) |
| `--cudn-churn-cycles` | `0` | Number of [CUDN churn](#cudn-churn) cycles (0 = disabled) |
| `--cudn-churn-percent` | `10` | Percentage of CUDNs to churn per cycle (1-99) |
| `--cudn-churn-delay` | `2m` | Delay between CUDN churn cycles |
| `--metrics-profile` | `metrics.yml` | Comma-separated list of [metrics profiles](#metrics-profiles) to use |
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
| `--cudn-churn-percent` | **Medium** | Higher percentage churns more CUDNs per cycle, generating more OVN-K churn load |
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

> **Note:** With `--gc=true`, this is handled automatically by the cleanup jobs. When CUDN churn is enabled, cleanup runs as a separate Phase 3 after all churn cycles complete.

---

## File Inventory

### Job Configuration

| File | Description |
|------|-------------|
| [`cudn-density.yml`](cudn-density.yml) | Main job configuration with setup, churn recreate, and cleanup sections |

### Go Source

| File | Description |
|------|-------------|
| `pkg/workloads/cudn-density.go` | CLI flags, validation, and 3-phase orchestration for CUDN churn |
| `pkg/workloads/cudn-churn.go` | CUDN churn deletion logic (finalizer-aware namespace + CUDN deletion) |

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
