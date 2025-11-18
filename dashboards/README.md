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

## Importing Dashboards

To import a dashboard into Grafana:

1. Navigate to your Grafana instance
2. Go to **Dashboards** → **Import**
3. Upload the JSON file or paste its contents
4. Click **Import**

## Datasource Configuration

Both dashboards require either Elasticsearch or OpenSearch datasources to be configured in Grafana.

> [!NOTE]  
> Remember to use `timestamp` as Time field name in the datasource

## Metrics Profiles Location

The metrics profiles referenced above can be found in the [metrics-profiles](https://github.com/kube-burner/kube-burner-ocp/tree/master/cmd/config/metrics-profiles) directory.

