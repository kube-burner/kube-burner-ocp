#!/usr/bin/env bats
# vi: ft=bash
# shellcheck disable=SC2086,SC2164

load helpers.bash

setup_file() {
  cd ocp
  export BATS_TEST_TIMEOUT=600
  export ES_SERVER="$PERFSCALE_PROD_ES_SERVER"
  export ES_INDEX="kube-burner-ocp"
  trap print_events ERR
  setup-prometheus
}

setup() {
  export UUID; UUID=$(uuidgen)
  export RATE="--qps=10 --burst=10"
  export COMMON_FLAGS="--es-server=${ES_SERVER} --es-index=${ES_INDEX} --alerting=true ${RATE}"
}

teardown() {
  oc delete ns -l kube-burner-uuid="${UUID}" --ignore-not-found
}

teardown_file() {
  $OCI_BIN rm -f prometheus
}

@test "custom-workload as node-density" {
  run_cmd kube-burner-ocp init --config=custom-workload.yml --metrics-endpoint metrics-endpoints.yaml --uuid=${UUID}
  check_metric_value jobSummary podLatencyMeasurement podLatencyQuantilesMeasurement
}

@test "node-density: es-indexing=true" {
  run_cmd kube-burner-ocp node-density --pods-per-node=75 --pod-ready-threshold=10s --uuid=${UUID} ${COMMON_FLAGS}
  check_metric_value etcdVersion jobSummary podLatencyMeasurement podLatencyQuantilesMeasurement
}

@test "node-density-heavy: gc-metrics=true; local-indexing=true" {
  run_cmd kube-burner-ocp node-density-heavy --pods-per-node=75 --uuid=abcd --local-indexing --gc-metrics=true
  check_file_list collected-metrics-abcd/etcdVersion.json collected-metrics-abcd/jobSummary.json collected-metrics-abcd/podLatencyMeasurement-node-density-heavy.json collected-metrics-abcd/podLatencyQuantilesMeasurement-node-density-heavy.json
}

@test "cluster-density-ms: metrics-endpoint=true; es-indexing=true" {
  run_cmd kube-burner-ocp cluster-density-ms --iterations=1 --churn=false --metrics-endpoint metrics-endpoints.yaml --uuid=${UUID}
  check_metric_value jobSummary podLatencyMeasurement podLatencyQuantilesMeasurement
}

@test "cluster-density-v2: profile-type=both; user-metadata=true; es-indexing=true; churning=true; svcLatency=true" {
  run_cmd kube-burner-ocp cluster-density-v2 --iterations=2 --churn-duration=1m --churn-delay=5s --profile-type=both ${COMMON_FLAGS} --user-metadata=user-metadata.yml --service-latency --uuid=${UUID}
  check_metric_value cpu-kubelet jobSummary podLatencyMeasurement podLatencyQuantilesMeasurement svcLatencyMeasurement svcLatencyQuantilesMeasurement etcdVersion 
}

@test "cluster-density-v2: churn-deletion-strategy=gvr; custom-metrics=true" {
  run_cmd kube-burner-ocp cluster-density-v2 --iterations=2 --churn=true --churn-duration=1m --churn-delay=5s --churn-deletion-strategy=gvr --metrics-profile=custom-metrics.yml ${COMMON_FLAGS} --uuid=${UUID}
  check_metric_value prometheusRSS jobSummary podLatencyMeasurement podLatencyQuantilesMeasurement
}

@test "cluster-density-v2: indexing=false; churning=false" {
  run_cmd kube-burner-ocp cluster-density-v2 --iterations=2 --churn=false --uuid=${UUID}
}

@test "node-density-cni: gc=false; alerting=false" {
  # Disable gc and avoid metric indexing
  run_cmd kube-burner-ocp node-density-cni --pods-per-node=75 --gc=false --uuid=${UUID} --alerting=false
  oc delete ns -l kube-burner-uuid=${UUID} --wait=false
  trap - ERR
}

@test "cluster-density-v2 timeout check" {
  run timeout 10s kube-burner-ocp cluster-density-v2 --iterations=1 --churn-duration=5m --timeout=1s
  [ "$status" -eq 2 ]
}

@test "index: local-indexing=true" {
  run_cmd kube-burner-ocp index --uuid=${UUID} --metrics-profile "custom-metrics.yml"
}

@test "index: metrics-endpoints=true; es-indexing=true" {
  run_cmd kube-burner-ocp index --uuid="${UUID}" --metrics-endpoint metrics-endpoints.yaml --metrics-profile metrics.yml --es-server=https://search-perfscale-dev-chmf5l4sh66lvxbnadi4bznl3a.us-west-2.es.amazonaws.com:443 --es-index=ripsaw-kube-burner --user-metadata user-metadata.yml
}

@test "networkpolicy-multitenant" {
  run_cmd kube-burner-ocp networkpolicy-multitenant --iterations 5 ${COMMON_FLAGS} --uuid=${UUID}
}

@test "crd-scale; alerting=false" {
  run_cmd kube-burner-ocp crd-scale --iterations=10 --alerting=false
}

@test "cluster-health" {
  run_cmd kube-burner-ocp cluster-health
}
