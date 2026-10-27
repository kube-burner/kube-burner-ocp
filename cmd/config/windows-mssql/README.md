# windows-mssql Workload

TPC-C database performance benchmark using HammerDB against Microsoft SQL Server on Windows VMs.

## Prerequisites

- Windows Server image with SQL Server pre-installed (QCOW2 format)
- HammerDB 4.12+ installed at `C:\tools\hammerdb-4.12` in the image
- SSH server enabled in Windows image with key at `~/.ssh/windows-mssql-id_rsa`
- Storage class supporting Block mode PVCs
- virtctl CLI tool installed

## Quick Start Examples

### LVMS Storage (1 VM)
```bash
kube-burner-ocp windows-mssql \
  --windows-image-url http://localhost:8083/windows_server_2k25.qcow2 \
  --total-vms 1 \
  --storage-class lvms-nvme \
  --access-mode RWO \
  --per-node-dv \
  --eviction-strategy None \
  --vm-cpu 32 \
  --vm-memory 32Gi \
  --local-indexing
```

### ODF Storage (1 VM)
```bash
kube-burner-ocp windows-mssql \
  --windows-image-url http://localhost:8083/windows_server_2k25.qcow2 \
  --total-vms 1 \
  --storage-class ocs-storagecluster-ceph-rbd \
  --access-mode RWX \
  --use-snapshot \
  --vm-cpu 32 \
  --vm-memory 32Gi \
  --local-indexing
```

### LVMS Scale (All Nodes)
```bash
kube-burner-ocp windows-mssql \
  --windows-image-url http://localhost:8083/windows_server_2k25.qcow2 \
  --vms-per-node 1 \
  --storage-class lvms-nvme \
  --access-mode RWO \
  --per-node-dv \
  --eviction-strategy None \
  --vm-cpu 32 \
  --vm-memory 32Gi \
  --local-indexing
```

### ODF Scale (3 VMs)
```bash
kube-burner-ocp windows-mssql \
  --windows-image-url http://localhost:8083/windows_server_2k25.qcow2 \
  --total-vms 3 \
  --storage-class ocs-storagecluster-ceph-rbd \
  --access-mode RWX \
  --use-snapshot \
  --vm-cpu 32 \
  --vm-memory 32Gi \
  --local-indexing
```

**Key Differences:**
- `--vms-per-node 1`: Creates 1 VM on every worker node using nodeSelector (auto-scales)
- `--total-vms 3`: Creates exactly 3 VMs total using nodeSelector and `--node-distribution` (fixed count)
- LVMS: Use `--per-node-dv`, `--access-mode RWO`, `--eviction-strategy None`
- ODF: Use `--use-snapshot`, `--access-mode RWX`

---

## Configuration Options

### All Available Flags

| Flag | Default | Description |
|------|---------|-------------|
| **Required** | | |
| `--windows-image-url` | - | HTTP/S3 URL to Windows QCOW2 disk image with SQL Server |
| **VM Configuration** | | |
| `--vms-per-node` | `1` | Number of VMs to create per worker node |
| `--total-vms` | `0` | Total VMs to create (overrides vms-per-node if set) |
| `--vm-cpu` | `16` | CPU cores per VM |
| `--vm-cpu-request` | `0` | CPU request per VM (defaults to vm-cpu if 0) |
| `--vm-memory` | `16Gi` | Memory per VM |
| `--node-distribution` | `sequential` | VM placement: `sequential` (fill nodes) or `round-robin` |
| `--eviction-strategy` | `LiveMigrate` | VM eviction: `LiveMigrate` or `None` (required for LVMS) |
| **Storage Configuration** | | |
| `--storage-class` | auto-detect | Storage class name |
| `--access-mode` | `RWX` | PVC access mode: `RO`, `RWO`, `RWX` |
| `--root-disk-size` | `76Gi` | Root disk size (Windows + SQL Server) |
| `--data-disk-size` | `76Gi` | Data disk size (TPC-C database) |
| `--use-snapshot` | `false` | Use VolumeSnapshot for cloning (ODF recommended) |
| `--per-node-dv` | `false` | Create source DV per node (LVMS required) |
| `--cdi-source-type` | `http` | CDI source: `http` or `s3` |
| `--cdi-source-s3-cred` | `""` | Secret name for S3 authentication |
| **Database Configuration** | | |
| `--db-users` | `1,2,4,8,16,32` | Comma-separated HammerDB user counts to test |
| `--db-warehouses` | `96` | TPC-C warehouse count (database size) |
| `--db-transactions` | `100000` | Transactions per test run |
| `--db-iterations` | `1` | Benchmark repetitions per user count |
| `--db-rampup` | `1m` | Benchmark rampup time |
| `--db-runtime` | `1m` | Benchmark runtime per iteration |
| **Workload Settings** | | |
| `--namespace` | `windows-mssql` | Namespace for VMs and resources |
| `--source-dv-name` | `windows-mssql-source-dv` | Source DataVolume name prefix |
| `--clear-node-caches` | `true` | Clear worker node buffer caches before benchmark |
| `--bulk-sleep` | `30s` | Rest duration between VM start bulks |
| `--ssh-concurrency-limit` | `20` | Concurrent SSH operations |
| `--metrics-profile` | `metrics.yml` | Comma-separated metrics profiles |
| `--local-indexing` | `false` | Write results to local directory |

**Important Notes:**
- `--use-snapshot` and `--per-node-dv` are **mutually exclusive**
  - Use `--use-snapshot` for ODF/Ceph (network storage)
  - Use `--per-node-dv` for LVMS (node-local storage)
- LVMS requires: `--access-mode RWO`, `--eviction-strategy None`, `--per-node-dv`
- ODF recommended: `--access-mode RWX`, `--use-snapshot`

---