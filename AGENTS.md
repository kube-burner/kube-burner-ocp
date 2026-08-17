# AGENTS.md — kube-burner-ocp

This file provides guidance to AI agents when working with code in this repository.

## Project overview

**kube-burner-ocp** is an OpenShift-specific plugin for [kube-burner](https://github.com/kube-burner/kube-burner), a Kubernetes load/stress testing framework. It ships pre-built workload scenarios for benchmarking OpenShift clusters.

Module: `github.com/kube-burner/kube-burner-ocp`
Core dependency: `github.com/kube-burner/kube-burner/v2`

## Repository layout

```
cmd/ocp.go                      # Single entry point; registers all workload subcommands (cobra)
cmd/config/<workload>/          # ~40+ workload config dirs with Go-template YAML files
cmd/config/metrics-profiles/    # Prometheus metrics collection profiles
cmd/config/alerts-profiles/     # Alert rule profiles
cmd/config/scripts/             # Helper scripts used by workloads
pkg/workloads/                  # Go files defining each workload's cobra command, flags, and setup
pkg/workloads/helpers.go        # Shared utilities (metadata, metrics setup, template var defaults)
pkg/workloads/types.go          # Type definitions (ProfileType constants)
pkg/clusterhealth/              # Cluster health check logic
pkg/measurements/               # OCP-specific measurements (Linux build tags: _linux.go/_unspecified.go)
test/test-ocp.bats              # Integration tests (BATS)
test/helpers.bash               # BATS helper functions
hack/                           # Build/CI scripts, test mappings, license checks
dashboards/                     # Grafana dashboard JSON files
.github/                        # GitHub Actions workflows
README.md                       # Project documentation
```

`cmd/config/` is embedded at compile time via `//go:embed`. Changes require rebuild.

## Build / test / lint

```bash
make build              # Build binary → bin/<arch>/kube-burner-ocp
make lint               # pre-commit run --all-files
make test               # BATS integration tests (needs live OCP cluster)
```

## Adding a new workload (the canonical pattern)

Every workload follows this rigid structure:

1. **Go file** in `pkg/workloads/` — define `New<Workload>(wh *workloads.WorkloadHelper, ...) *cobra.Command`:
   - Cobra flags define workload-specific parameters (kebab-case: `--pod-ready-threshold`)
   - `Run` callback sets `AdditionalVars["UPPER_SNAKE_CASE"] = value` for each flag
   - Call `setMetrics(cmd, metricsProfiles)` then `RunWorkload(cmd, wh, "config-file.yml")`
   - `PostRun` calls `os.Exit(rc)`
   - If config dir name differs from command name, set `cmd.Annotations["configDir"]`

2. **Config directory** in `cmd/config/<workload>/` — YAML templates using `{{.VARIABLE_NAME}}` matching `AdditionalVars` keys.

3. **Registration** in `cmd/ocp.go` — add to `ocpCmd.AddCommand(...)`.

4. **Test** in `test/test-ocp.bats` — tag with `# bats test_tags=workload:<name>` and add mapping in `hack/bats_test_mappings.yml`.

## Code conventions

- **Logging:** `logrus` exclusively (imported as `log`).
- **Error handling:** `log.Fatal`/`log.Fatalf` for unrecoverable errors. No custom error types.
- **Imports:** stdlib, external, internal (standard Go grouping).
- **License:** Apache 2.0 header on all Go files.
- **Testing:** No Go unit tests for most code. Testing is integration-only via BATS against a live cluster.

## CI

GitHub Actions: lint, CodeQL, multi-arch build matrix, E2E tests on real OCP cluster (gated by `ok-to-test` label).
