#!/usr/bin/env bats
# vi: ft=bash
# shellcheck disable=SC2086,SC2164

load helpers.bash

setup_file() {
  cd ocp
  export BATS_TEST_TIMEOUT=1800
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
  run_cmd kube-burner-ocp node-density --pods-per-node=75 --pod-ready-threshold=1m --uuid=${UUID} ${COMMON_FLAGS} --churn=true --churn-duration=1m --churn-delay=5s
  check_metric_value etcdVersion jobSummary podLatencyMeasurement podLatencyQuantilesMeasurement
}

@test "node-density-heavy: gc-metrics=true; local-indexing=true" {
  run_cmd kube-burner-ocp node-density-heavy --pods-per-node=75 --pod-ready-threshold=1m --uuid=abcd --local-indexing --gc-metrics=true --churn=true --churn-cycles=2 --churn-delay=5s
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
  # ENsure indexing variables are not set
  unset ES_SERVER ES_INDEX
  run_cmd kube-burner-ocp cluster-density-v2 --iterations=2 --churn=false --uuid=${UUID}
}

@test "node-density-cni: gc=false; alerting=false" {
  # Disable gc and avoid metric indexing
  run_cmd kube-burner-ocp node-density-cni --pods-per-node=75 --gc=false --uuid=${UUID} --alerting=false --churn=true --churn-cycles=2 --churn-delay=5s
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
  run_cmd kube-burner-ocp index --uuid="${UUID}" --metrics-endpoint metrics-endpoints.yaml --metrics-profile metrics.yml --user-metadata user-metadata.yml
}

@test "networkpolicy" {
  run_cmd kube-burner-ocp network-policy --iterations 2 ${COMMON_FLAGS} --uuid=${UUID}
}

@test "whereabouts" {
  run_cmd kube-burner-ocp whereabouts --iterations 2 --pod-ready-threshold=1m ${COMMON_FLAGS} --uuid=${UUID}
}

@test "crd-scale; alerting=false" {
  run_cmd kube-burner-ocp crd-scale --iterations=2 --alerting=false
}

@test "virt-density" {
  run_cmd kube-burner-ocp virt-density --vms-per-node=5 --vmi-ready-threshold=1m --uuid=${UUID} ${COMMON_FLAGS}
  check_metric_value jobSummary vmiLatencyMeasurement vmiLatencyQuantilesMeasurement
}

@test "virt-udn-l2-density" {
  run_cmd kube-burner-ocp virt-udn-density --iteration 5 --layer3=false --binding-method=l2bridge ${COMMON_FLAGS} --uuid=${UUID}
}

@test "virt-udn-l3-density" {
  run_cmd kube-burner-ocp virt-udn-density --iteration 2 ${COMMON_FLAGS} --uuid=${UUID}
}

# This test is under the deprecation path and will be removed in a future update.
@test "web-burner-node-density" {
  LB_WORKER=$(oc get node | grep worker | head -n 1 | cut -f 1 -d' ')
  run_cmd oc label node $LB_WORKER node-role.kubernetes.io/worker-spk="" --overwrite
  run_cmd kube-burner-ocp web-burner-init --gc=false --sriov=false --bridge=br-ex --bfd=false --es-server="" --es-index="" --alerting=true --uuid=${UUID} ${RATE}
  run_cmd kube-burner-ocp web-burner-node-density --gc=false --probe=false --es-server="" --es-index="" --alerting=true --uuid=${UUID} ${RATE}
  check_running_pods kube-burner-job=init-served-job 1
  check_running_pods kube-burner-job=serving-job 4
  check_running_pods kube-burner-job=normal-job-1 60
  run_cmd oc delete project served-ns-0 serving-ns-0
}

# This test is under the deprecation path and will be removed in a future update.
@test "web-burner-cluster-density" {
  LB_WORKER=$(oc get node | grep worker | head -n 1 | cut -f 1 -d' ')
  run_cmd oc label node $LB_WORKER node-role.kubernetes.io/worker-spk="" --overwrite
  run_cmd kube-burner-ocp web-burner-init --gc=false --sriov=false --bridge=br-ex --bfd=false --es-server="" --es-index="" --alerting=true --uuid=${UUID} ${RATE}
  run_cmd kube-burner-ocp web-burner-cluster-density --gc=false --probe=false --es-server="" --es-index="" --alerting=true --uuid=${UUID} ${RATE}
  check_running_pods kube-burner-job=init-served-job 1
  check_running_pods kube-burner-job=serving-job 4
  check_running_pods kube-burner-job=cluster-density 35
  check_running_pods kube-burner-job=app-job-1 3
}

@test "cluster-health" {
  run_cmd kube-burner-ocp cluster-health
}

@test "virt-capacity-benchmark" {
  VIRT_CAPACITY_BENCHMARK_STORAGE_CLASS=${VIRT_CAPACITY_BENCHMARK_STORAGE_CLASS:-oci-bv}
  run_cmd kube-burner-ocp virt-capacity-benchmark --storage-class $VIRT_CAPACITY_BENCHMARK_STORAGE_CLASS --max-iterations 2  --data-volume-count 2 --vms 2 --skip-migration-job --skip-resize-job
  local jobs=("create-vms" "restart-vms")
  for job in "${jobs[@]}"; do
    check_metric_recorded ./virt-capacity-benchmark/iteration-1 ${job} vmiLatency vmReadyLatency
    check_quantile_recorded ./virt-capacity-benchmark/iteration-1 ${job} vmiLatency VMReady
  done
  oc delete namespace virt-capacity-benchmark
}

@test "virt-clone" {
  VIRT_CLONE_STORAGE_CLASS=${VIRT_CLONE_STORAGE_CLASS:-oci-bv}
  run_cmd kube-burner-ocp virt-clone --storage-class $VIRT_CLONE_STORAGE_CLASS --access-mode RWO --vms 2
  local jobs=("create-base-vm" "create-clone-vms")
  for job in "${jobs[@]}"; do
    check_metric_recorded ./virt-clone-results ${job} dvLatency dvReadyLatency
    check_metric_recorded ./virt-clone-results ${job} vmiLatency vmReadyLatency
    check_quantile_recorded ./virt-clone-results ${job} dvLatency Ready
    check_quantile_recorded ./virt-clone-results ${job} vmiLatency VMReady
  done
  run_cmd oc delete ns -l kube-burner.io/test-name=virt-clone
}

@test "pvc-density" {
  PVC_DENSITY_PROVISIONER=${PVC_DENSITY_PROVISIONER:-oci}
  run_cmd kube-burner-ocp pvc-density --iterations=2 --provisioner $PVC_DENSITY_PROVISIONER
}
