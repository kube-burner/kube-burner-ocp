# Grafana Dashboards

This directory contains Grafana dashboard JSON files for visualizing kube-burner metrics and performance data.

## Dashboards

### Kube-burner.json

The `Kube-burner.json` dashboard is designed to visualize metrics collected using the standard kube-burner metrics profiles.

**Compatible Metrics Profiles:**

- `metrics.yml`
- `metrics-aggregated.yml`

### Kube-burner-report-profile.json

The `Kube-burner-report-profile.json` dashboard is designed to visualize metrics collected using the report metrics profile.

**Compatible Metrics Profiles:**

- `metrics-report.yml`

### Virt-capacity-benchmark.json

The `Virt-capacity-benchmark.json` dashboard is designed to visualize the results of the **virt-capacity-benchmark** workload, providing an overview of performance and metrics collected during its execution.

### Virt-clone.json

The `Virt-clone.json` dashboard is designed to visualize the results of the **virt-clone** workload, providing an overview of performance and metrics collected during its execution.

### Virt-density.json

The `Virt-density.json` dashboard is designed to visualize the results of the **virt-density** workload, providing an overview of performance and metrics collected during its execution.

### Virt-udn-density.json

The `Virt-udn-density.json` dashboard is designed to visualize the results of the **virt-udn-density** workload, providing an overview of performance and metrics collected during its execution.

### Virt-ephemeral-restart.json

The `Virt-ephemeral-restart.json` dashboard is designed to visualize the results of the **virt-ephemeral-restart** workload, providing an overview of performance and metrics collected during its execution.

### Virt-migration.json

The `Virt-migration.json` dashboard is designed to visualize the results of the **virt-migration** workload, providing an overview of performance and metrics collected during its execution.

## Importing Dashboards

To import a dashboard into Grafana:

1. Navigate to your Grafana instance
2. Go to **Dashboards** → **Import**
3. Upload the JSON file or paste its contents
4. Click **Import**

## Datasource Configuration

All dashboards require either Elasticsearch or OpenSearch datasources to be configured in Grafana.

> [!NOTE]  
> Remember to use `timestamp` as Time field name in the datasource

## Metrics Profiles Location

The metrics profiles referenced above can be found in the [metrics-profiles](https://github.com/kube-burner/kube-burner-ocp/tree/master/cmd/config/metrics-profiles) directory.

