#!/usr/bin/env bats
# vi: ft=bash
# shellcheck disable=SC2086,SC2164

load helpers.bash

setup_file() {
  cd ocp
  export BATS_TEST_TIMEOUT=600
  export ES_SERVER="https://search-perfscale-dev-chmf5l4sh66lvxbnadi4bznl3a.us-west-2.es.amazonaws.com"
  export ES_INDEX="kube-burner-ocp"
  trap print_events ERR
  setup-prometheus
}

setup() {
  export UUID; UUID=$(uuidgen)
  export COMMON_FLAGS="--es-server=${ES_SERVER} --es-index=${ES_INDEX} --alerting=true --qps=5 --burst=5"
}

teardown() {
  oc delete ns -l kube-burner-uuid="${UUID}" --ignore-not-found
  # web-burner workload specific
  oc label node -l node-role.kubernetes.io/worker-spk= node-role.kubernetes.io/worker-spk-
  oc delete AdminPolicyBasedExternalRoute --all
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
  oc delete ns -l kube-burner-uuid=${UUID}
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
  run_cmd kube-burner-ocp index --uuid="${UUID}" --metrics-endpoint metrics-endpoints.yaml --metrics-profile metrics.yml --es-server=https://search-perfscale-dev-chmf5l4sh66lvxbnadi4bznl3a.us-west-2.es.amazonaws.com:443 --es-index=ripsaw-kube-burner
}

@test "networkpolicy-multitenant" {
  run_cmd kube-burner-ocp networkpolicy-multitenant --iterations 5 ${COMMON_FLAGS} --uuid=${UUID}
}

@test "pvc-density" {
  # Since 'aws' is the chosen storage provisioner, this will only execute successfully if the ocp environment is aws
  run_cmd kube-burner-ocp pvc-density --iterations=2 --provisioner=aws ${COMMON_FLAGS} --uuid=${UUID}
  check_metric_value jobSummary podLatencyMeasurement podLatencyQuantilesMeasurement
}

@test "web-burner-node-density" {
  LB_WORKER=$(oc get node | grep worker | head -n 1 | cut -f 1 -d' ')
  run_cmd oc label node $LB_WORKER node-role.kubernetes.io/worker-spk="" --overwrite
  run_cmd kube-burner-ocp web-burner-init --gc=false --sriov=false --bridge=br-ex --bfd=false --es-server="" --es-index="" --alerting=true --uuid=${UUID} --qps=5 --burst=5
  run_cmd kube-burner-ocp web-burner-node-density --gc=false --probe=false --es-server="" --es-index="" --alerting=true --uuid=${UUID} --qps=5 --burst=5
  check_running_pods kube-burner-job=init-served-job 1
  check_running_pods kube-burner-job=serving-job 4
  check_running_pods kube-burner-job=normal-job-1 60
  run_cmd oc delete project served-ns-0 serving-ns-0
}

@test "web-burner-cluster-density" {
  LB_WORKER=$(oc get node | grep worker | head -n 1 | cut -f 1 -d' ')
  run_cmd oc label node $LB_WORKER node-role.kubernetes.io/worker-spk="" --overwrite
  run_cmd kube-burner-ocp web-burner-init --gc=false --sriov=false --bridge=br-ex --bfd=false --es-server="" --es-index="" --alerting=true --uuid=${UUID} --qps=5 --burst=5
  run_cmd kube-burner-ocp web-burner-cluster-density --gc=false --probe=false --es-server="" --es-index="" --alerting=true --uuid=${UUID} --qps=5 --burst=5
  check_running_pods kube-burner-job=init-served-job 1
  check_running_pods kube-burner-job=serving-job 4
  check_running_pods kube-burner-job=cluster-density 35
  check_running_pods kube-burner-job=app-job-1 3
}

@test "cluster-health" {
  run_cmd kube-burner-ocp cluster-health
}


