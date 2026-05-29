# UDN BGP Route Advertisement Latency Measurement

This document provides a detailed technical explanation of how the `raLatency` measurement works in the `udn-bgp` workload. This measurement tests BGP route exchange between OpenShift Cluster User Defined Networks (CUDN) and external hosts.

## Overview

The measurement captures latency for two BGP route exchange scenarios:

| Scenario | Direction | Description |
|----------|-----------|-------------|
| **Export** | Cluster → External | CUDN subnets advertised via RouteAdvertisement to external host |
| **Import** | External → Cluster | External routes imported into CUDN gateway routers |

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         OpenShift Cluster                                   │
│  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐                    │
│  │   CUDN-0     │   │   CUDN-1     │   │   CUDN-N     │                    │
│  │ 40.0.0.0/16  │   │ 40.1.0.0/16  │   │ ...          │                    │
│  │  ┌────────┐  │   │  ┌────────┐  │   │              │                    │
│  │  │  Pod   │  │   │  │  Pod   │  │   │              │  ◄── RouteAdvert.  │
│  │  └────────┘  │   │  └────────┘  │   │              │       exports these│
│  └──────────────┘   └──────────────┘   └──────────────┘       subnets      │
│                            │                                                │
│                     OVN + Internal FRR                                      │
└────────────────────────────┼────────────────────────────────────────────────┘
                             │ BGP Session
┌────────────────────────────▼────────────────────────────────────────────────┐
│                    External Host (kube-burner)                              │
│                                                                             │
│  EXPORT: Receives CUDN routes     │  IMPORT: Generates routes on dummy     │
│  via BGP, adds to routing table   │  interfaces, advertised via BGP        │
│                                   │                                         │
│  ┌─────────────────────────────┐  │  ┌─────────────────────────────┐       │
│  │ Netlink Route Subscription  │  │  │ dummy0: 20.0.1.1/24         │       │
│  │ (monitors new routes)       │  │  │ dummy1: 20.1.1.1/24         │       │
│  │                             │  │  │ ...                          │       │
│  └─────────────────────────────┘  │  └─────────────────────────────┘       │
│                                   │                                         │
│                     External FRR Router                                     │
└─────────────────────────────────────────────────────────────────────────────┘
```

## Workload Jobs

The `udn-bgp` workload creates resources in this order:

| Job | Name | Purpose |
|-----|------|---------|
| 1 | `udn-bgp-create-namespaces` | Creates N namespaces (`udn-bgp-0`, `udn-bgp-1`, ...) with required labels |
| 2 | `udn-bgp-create-cudn` | Creates ClusterUserDefinedNetworks (each CUDN spans multiple namespaces) |
| 3 | `udn-bgp-create-pods` | Creates pods (or VMs with `--vm` flag) attached to CUDN networks |
| 4 | `udn-bgp-route-advertisements` | Creates RouteAdvertisement CRDs to export CUDN subnets via BGP |

Only Job 4 has measurements enabled - the latency measurement starts when RouteAdvertisements are created.

## Measurement Lifecycle

### Start Phase

When the measurement starts (before Job 4 creates RouteAdvertisements):

1. **Read configuration** from job input variables:
   - `numDummyIfaces`: Number of dummy interfaces for import scenario (default: 10)
   - `numAddressOnDummyIface`: IP addresses per interface (default: 20)
   - `exportScenarioMaxTimeout`: Max wait time for export (default: 10m)
   - `importScenarioMaxTimeout`: Max wait time for import (default: 10m)

2. **Build CUDN → Pod mapping** by scanning pod annotations:
   ```
   cudnSubnet["40.0.0.0/16"] = {cudn: "cudn-0", pods: ["40.0.1.5"]}
   cudnSubnet["40.1.0.0/16"] = {cudn: "cudn-1", pods: ["40.1.2.3"]}
   ```

3. **Start export scenario monitoring**:
   - Subscribe to kernel netlink route notifications
   - Start 20 export worker goroutines
   - Register Kubernetes informer for RouteAdvertisement events

## Export Scenario (Cluster → External)

**Goal**: Measure latency from RouteAdvertisement creation until the external host can ping CUDN pods.

### Timeline

```
─────────────────────────────────────────────────────────────────────────►
  │                     │                              │
  T0: RA created       T1: Route arrives              T2: Ping success
  (API timestamp)       (netlink notification)          (pod reachable)

  ◄───────────────────────────────────────────────────►
              Total Export Latency (T2 - T0)

  ◄─────────────────────►
     NetlinkRouteLatency (T1 - T0)
```

### Flow

1. **RouteAdvertisement Informer** (`handleAdd`):
   - Detects new RA creation via Kubernetes API
   - Records RA creation timestamp from metadata
   - Maps RA → list of CUDNs it advertises

2. **Netlink Route Subscription**:
   - Kernel notifies when BGP (via external FRR) adds routes to host routing table
   - Each route corresponds to a CUDN subnet being exported

3. **Export Workers** (20 goroutines):
   - Read route updates from netlink channel
   - Match route to CUDN subnet from the pre-built map
   - Record netlink route detection timestamp
   - Ping corresponding CUDN pods (up to 100 attempts, 100ms timeout per attempt)
   - Record ping success timestamp

### Metrics Captured

| Metric | Description |
|--------|-------------|
| `NetlinkRouteLatency` | Time from RA creation → route appears in kernel |
| `ReadyLatency` | Time from RA creation → successful ping to CUDN pod |

## Import Scenario (External → Cluster)

**Goal**: Measure latency for external routes to be imported into cluster CUDNs.

### Timeline

```
─────────────────────────────────────────────────────────────────────────►
  │                                                     │
  T0: IP added to                                      T1: Ping success
  dummy interface                                       (from external IP
  (route auto-generated)                                to CUDN pod)

  ◄───────────────────────────────────────────────────►
              Import Latency (T1 - T0)
```

### Flow

1. **Create Dummy Interfaces**:
   - Creates `dummy0`, `dummy1`, ... `dummyN` network interfaces
   - Number controlled by `numDummyIfaces` (default: 10)

2. **Generate Routes**:
   - Adding an IP address to a dummy interface auto-generates a Linux route
   - External FRR advertises these routes to the cluster
   - Routes follow pattern: `20.{iface}.{addr}.1/24`
   - Total routes = `numDummyIfaces × numAddressOnDummyIface` (default: 200)

3. **Import Workers** (10 goroutines):
   - Read work items from channel (interface name + IP address + pods to ping)
   - Add IP address to dummy interface (creates route)
   - Wait for BGP to advertise route to cluster
   - Ping all CUDN pods using the new IP as **source address**
   - Record latency when ping succeeds

### Why Source IP Matters

The import scenario uses the newly added IP as the ping source address. This ensures the test validates that:
- The external route was properly imported into CUDN gateway routers
- Return traffic correctly routes back through the imported route

### Metrics Captured

| Metric | Description |
|--------|-------------|
| `ReadyLatency` | Time from IP/route creation → successful ping using that source IP |

## Stop Phase

When the measurement stops (after Job 4 completes):

1. **Wait for export scenario completion**:
   - Polls until all CUDN subnets have been verified (routes received + ping successful)
   - Times out after `exportScenarioMaxTimeout`

2. **Close export workers** and stop netlink subscription

3. **Start import scenario**:
   - Create dummy interfaces
   - Queue work items for import workers
   - Start 10 import worker goroutines

4. **Wait for import scenario completion**:
   - Polls until all generated routes are verified
   - Times out after `importScenarioMaxTimeout`

5. **Cleanup**:
   - Delete all dummy interfaces
   - Normalize and index metrics

## Configuration Parameters

These can be set in the job configuration:

| Parameter | Default | Description |
|-----------|---------|-------------|
| `numDummyIfaces` | 10 | Number of dummy interfaces for import |
| `numAddressOnDummyIface` | 20 | IP addresses per interface |
| `exportScenarioMaxTimeout` | 10m | Max wait for export validation |
| `importScenarioMaxTimeout` | 10m | Max wait for import validation |

## Indexed Metrics

### Per-Route Metrics (raLatencyMeasurement)

**Export scenario** (one document per RouteAdvertisement):

```json
{
  "timestamp": "2025-04-15T08:41:10Z",
  "metricName": "raLatencyMeasurement",
  "routeAdvertisementName": "ra-0",
  "scenario": "ExportRoutes",
  "latency": [10031],
  "minReadyLatency": 10031,
  "maxReadyLatency": 10031,
  "p99ReadyLatency": 10031,
  "netlinkRouteLatency": [10026],
  "minNetlinkRouteLatency": 10026,
  "maxNetlinkRouteLatency": 10026,
  "p99NetlinkRouteLatency": 10026
}
```

**Import scenario** (one document per generated route):

```json
{
  "timestamp": "2025-04-15T08:42:20Z",
  "metricName": "raLatencyMeasurement",
  "routeAdvertisementName": "20.0.1.1/24",
  "scenario": "ImportRoutes",
  "latency": [13],
  "minReadyLatency": 13,
  "maxReadyLatency": 13,
  "p99ReadyLatency": 13
}
```

### Quantile Summary (raLatencyQuantilesMeasurement)

Aggregated statistics across all routes:

| Metric | Description |
|--------|-------------|
| `MinReadyLatency` | Minimum ping latency |
| `MaxReadyLatency` | Maximum ping latency |
| `P99ReadyLatency` | 99th percentile ping latency |
| `MinNetlinkRouteLatency` | Minimum route detection latency (export only) |
| `MaxNetlinkRouteLatency` | Maximum route detection latency (export only) |
| `P99NetlinkRouteLatency` | 99th percentile route detection latency (export only) |

## Code Reference

- **Measurement implementation**: `pkg/measurements/routeadvertisement-latency_linux.go`
- **Workload configuration**: `cmd/config/udn-bgp/udn-bgp.yml`
- **Resource templates**:
  - `cmd/config/udn-bgp/cudn.yml` - ClusterUserDefinedNetwork
  - `cmd/config/udn-bgp/ra.yml` - RouteAdvertisement
  - `cmd/config/udn-bgp/pod.yml` - Pod template
  - `cmd/config/udn-bgp/vm.yml` - VirtualMachine template
