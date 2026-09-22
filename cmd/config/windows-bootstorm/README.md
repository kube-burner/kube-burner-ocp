# windows-bootstorm Workload

Measures Windows VM boot time at scale. Creates VMs from a Windows disk image, starts them in parallel, and measures SSH accessibility time per VM.

## Usage

### ODF (Ceph) — default

```bash
kube-burner-ocp windows-bootstorm \
  --windows-image-url http://example.com/windows11.qcow2 \
  --vms-per-node 37 \
  --local-indexing
```

### LVMS (local storage)

```bash
kube-burner-ocp windows-bootstorm \
  --windows-image-url http://example.com/windows11.qcow2 \
  --vms-per-node 37 \
  --storage-class lvms-nvme \
  --access-mode RWO \
  --eviction-strategy None \
  --per-node-dv \
  --local-indexing
```

### With OpenSearch indexing

Add to any of the above:
```bash
  --es-server 'https://user:pass@search-host:443' \
  --es-index ripsaw-kube-burner
```

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--windows-image-url` | *(required)* | URL to Windows qcow2 disk image |
| `--vms-per-node` | `1` | Number of VMs per worker node |
| `--storage-class` | cluster default | StorageClass for DVs |
| `--access-mode` | `ReadWriteMany` | PVC access mode (`RWX` for ODF, `RWO` for LVMS) |
| `--eviction-strategy` | `LiveMigrate` | VM eviction strategy (`None` for LVMS) |
| `--per-node-dv` | `false` | Create one source DV per worker node (required for LVMS) |
| `--source-dv-name` | `windows-bootstorm-source-dv` | Base name for source DataVolumes |
| `--namespace` | `windows-bootstorm` | Namespace for VMs |
| `--vm-cpu` | `1` | CPU cores per VM |
| `--vm-cpu-request` | `0` | CPU request per VM (defaults to `--vm-cpu` if unset) |
| `--vm-memory` | `2G` | Memory per VM |
| `--clear-node-caches` | `true` | Clear worker node caches before measurement |
| `--node-distribution` | `sequential` | VM distribution across nodes |

## Measurements

| metricName | Content |
|---|---|
| `bootstormSSHMeasurement` | Per-VM: `bootstorm_time`, `total_run_time`, `access_vm`, `node` |
| `dvLatencyQuantilesMeasurement` | P50/P95/P99 for DV phases (Bound/Running/Ready) |
| `dvLatencyMeasurement` | Per-DV raw latency |
| `jobSummary` | Job elapsed time, pass/fail, cluster metadata |

## LVMS Details

LVMS uses node-local storage. `--per-node-dv` creates one source DV per worker so all clones are same-node LVM thin snapshots (instant, no network copy). The workload auto-creates a temporary StorageClass with `cdi.kubevirt.io/clone-strategy: snapshot` and deletes it after the run.
