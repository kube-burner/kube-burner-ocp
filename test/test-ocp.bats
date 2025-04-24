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
  run_cmd ${KUBE_BURNER_OCP} init --config=custom-workload.yml --metrics-endpoint metrics-endpoints.yaml --uuid=${UUID}
  check_metric_value jobSummary podLatencyMeasurement podLatencyQuantilesMeasurement
}

@test "node-density: es-indexing=true" {
  run_cmd ${KUBE_BURNER_OCP} node-density --pods-per-node=75 --pod-ready-threshold=1m --uuid=${UUID} ${COMMON_FLAGS} --churn=true --churn-duration=1m --churn-delay=5s
  check_metric_value etcdVersion jobSummary podLatencyMeasurement podLatencyQuantilesMeasurement
}

@test "node-density-heavy: gc-metrics=true; local-indexing=true" {
  run_cmd ${KUBE_BURNER_OCP} node-density-heavy --pods-per-node=75 --pod-ready-threshold=1m --uuid=abcd --local-indexing --gc-metrics=true --churn=true --churn-cycles=2 --churn-delay=5s
  check_file_list collected-metrics-abcd/etcdVersion.json collected-metrics-abcd/jobSummary.json collected-metrics-abcd/podLatencyMeasurement-node-density-heavy.json collected-metrics-abcd/podLatencyQuantilesMeasurement-node-density-heavy.json
}

@test "cluster-density-ms: metrics-endpoint=true; es-indexing=true" {
  run_cmd ${KUBE_BURNER_OCP} cluster-density-ms --iterations=1 --churn=false --metrics-endpoint metrics-endpoints.yaml --uuid=${UUID}
  check_metric_value jobSummary podLatencyMeasurement podLatencyQuantilesMeasurement
}

@test "cluster-density-v2: profile-type=both; user-metadata=true; es-indexing=true; churning=true; svcLatency=true" {
  run_cmd ${KUBE_BURNER_OCP} cluster-density-v2 --iterations=2 --churn-duration=1m --churn-delay=5s --profile-type=both ${COMMON_FLAGS} --user-metadata=user-metadata.yml --service-latency --uuid=${UUID}
  check_metric_value cpu-kubelet jobSummary podLatencyMeasurement podLatencyQuantilesMeasurement svcLatencyMeasurement svcLatencyQuantilesMeasurement etcdVersion
}

@test "cluster-density-v2: churn-deletion-strategy=gvr; custom-metrics=true" {
  run_cmd ${KUBE_BURNER_OCP} cluster-density-v2 --iterations=2 --churn=true --churn-duration=1m --churn-delay=5s --churn-deletion-strategy=gvr --metrics-profile=custom-metrics.yml ${COMMON_FLAGS} --uuid=${UUID}
  check_metric_value prometheusRSS jobSummary podLatencyMeasurement podLatencyQuantilesMeasurement
}

@test "cluster-density-v2: indexing=false; churning=false" {
  # Ensure indexing variables are not set
  unset ES_SERVER ES_INDEX
  run_cmd ${KUBE_BURNER_OCP} cluster-density-v2 --iterations=2 --churn=false --uuid=${UUID}
}

@test "node-density-cni: gc=false; alerting=false" {
  # Disable gc and avoid metric indexing
  run_cmd ${KUBE_BURNER_OCP} node-density-cni --pods-per-node=75 --gc=false --uuid=${UUID} --alerting=false --churn=true --churn-cycles=2 --churn-delay=5s
  oc delete ns -l kube-burner-uuid=${UUID} --wait=false
  trap - ERR
}

@test "cluster-density-v2 timeout check" {
  run timeout 10s ${KUBE_BURNER_OCP} cluster-density-v2 --iterations=1 --churn-duration=5m --timeout=1s
  [ "$status" -eq 2 ]
}

@test "index: local-indexing=true" {
  run_cmd ${KUBE_BURNER_OCP} index --uuid=${UUID} --metrics-profile "custom-metrics.yml"
}

@test "index: metrics-endpoints=true; es-indexing=true" {
  run_cmd ${KUBE_BURNER_OCP} index --uuid="${UUID}" --metrics-endpoint metrics-endpoints.yaml --user-metadata user-metadata.yml
}

@test "networkpolicy" {
  run_cmd ${KUBE_BURNER_OCP} network-policy --iterations 2 ${COMMON_FLAGS} --uuid=${UUID}
}

@test "whereabouts" {
  run_cmd ${KUBE_BURNER_OCP} whereabouts --iterations 2 --pod-ready-threshold=1m ${COMMON_FLAGS} --uuid=${UUID}
}

@test "crd-scale; alerting=false" {
  run_cmd ${KUBE_BURNER_OCP} crd-scale --iterations=2 --alerting=false
}

@test "virt-density" {
  run_cmd ${KUBE_BURNER_OCP} virt-density --vms-per-node=5 --vmi-ready-threshold=1m --uuid=${UUID} ${COMMON_FLAGS}
  check_metric_value jobSummary vmiLatencyMeasurement vmiLatencyQuantilesMeasurement
}

@test "virt-udn-l2-density" {
  run_cmd ${KUBE_BURNER_OCP} virt-udn-density --iteration 5 --layer3=false --binding-method=l2bridge ${COMMON_FLAGS} --uuid=${UUID}
}

@test "virt-udn-l3-density" {
  run_cmd ${KUBE_BURNER_OCP} virt-udn-density --iteration 2 ${COMMON_FLAGS} --uuid=${UUID}
}

# This test is under the deprecation path and will be removed in a future update.
@test "web-burner-node-density" {
  LB_WORKER=$(oc get node | grep worker | head -n 1 | cut -f 1 -d' ')
  run_cmd oc label node $LB_WORKER node-role.kubernetes.io/worker-spk="" --overwrite
  run_cmd ${KUBE_BURNER_OCP} web-burner-init --gc=false --sriov=false --bridge=br-ex --bfd=false --es-server="" --es-index="" --alerting=true --uuid=${UUID} ${RATE}
  run_cmd ${KUBE_BURNER_OCP} web-burner-node-density --gc=false --probe=false --es-server="" --es-index="" --alerting=true --uuid=${UUID} ${RATE}
  check_running_pods kube-burner-job=init-served-job 1
  check_running_pods kube-burner-job=serving-job 4
  check_running_pods kube-burner-job=normal-job-1 60
  run_cmd oc delete project served-ns-0 serving-ns-0
}

# This test is under the deprecation path and will be removed in a future update.
@test "web-burner-cluster-density" {
  LB_WORKER=$(oc get node | grep worker | head -n 1 | cut -f 1 -d' ')
  run_cmd oc label node $LB_WORKER node-role.kubernetes.io/worker-spk="" --overwrite
  run_cmd ${KUBE_BURNER_OCP} web-burner-init --gc=false --sriov=false --bridge=br-ex --bfd=false --es-server="" --es-index="" --alerting=true --uuid=${UUID} ${RATE}
  run_cmd ${KUBE_BURNER_OCP} web-burner-cluster-density --gc=false --probe=false --es-server="" --es-index="" --alerting=true --uuid=${UUID} ${RATE}
  check_running_pods kube-burner-job=init-served-job 1
  check_running_pods kube-burner-job=serving-job 4
  check_running_pods kube-burner-job=cluster-density 35
  check_running_pods kube-burner-job=app-job-1 3
}

@test "cluster-health" {
  run_cmd ${KUBE_BURNER_OCP} cluster-health
}

@test "virt-capacity-benchmark" {
    local STORAGE_PARAMETER
  if [ -n "$KUBE_BURNER_OCP_STORAGE_CLASS" ]; then
    STORAGE_PARAMETER="--storage-class ${KUBE_BURNER_OCP_STORAGE_CLASS}"
  fi
  run_cmd ${KUBE_BURNER_OCP} virt-capacity-benchmark ${STORAGE_PARAMETER} --max-iterations 2  --data-volume-count 2 --vms 2 --skip-migration-job --skip-resize-job
  run_cmd kube-burner-ocp virt-capacity-benchmark --cleanup-only
  local jobs=("create-vms" "restart-vms")
  for job in "${jobs[@]}"; do
    check_metric_recorded ./virt-capacity-benchmark/iteration-1 ${job} vmiLatency vmReadyLatency
    check_quantile_recorded ./virt-capacity-benchmark/iteration-1 ${job} vmiLatency VMReady
  done
  check_destroyed_ns virt-capacity-benchmark
}

@test "virt-clone" {
  local STORAGE_PARAMETER
  if [ -n "$KUBE_BURNER_OCP_STORAGE_CLASS" ]; then
    STORAGE_PARAMETER="--storage-class ${KUBE_BURNER_OCP_STORAGE_CLASS}"
  fi
  run_cmd ${KUBE_BURNER_OCP} virt-clone ${STORAGE_PARAMETER} --access-mode RWO --vms 2
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
  run_cmd ${KUBE_BURNER_OCP} pvc-density --iterations=2 --provisioner $PVC_DENSITY_PROVISIONER
}

@test "virt-ephemeral-restart" {
  local STORAGE_PARAMETER
  if [ -n "$KUBE_BURNER_OCP_STORAGE_CLASS" ]; then
    STORAGE_PARAMETER="--storage-class ${KUBE_BURNER_OCP_STORAGE_CLASS}"
  fi
  run_cmd ${KUBE_BURNER_OCP} virt-ephemeral-restart ${STORAGE_PARAMETER} --access-mode RWO --iteration-vms 2 --iterations 2
  # Delete all resources before testing results to ensure they are deleted
  run_cmd oc delete ns -l kube-burner.io/test-name=virt-ephemeral-restart

  check_metric_recorded ./virt-ephemeral-restart-results start-vms dvLatency dvReadyLatency
  check_metric_recorded ./virt-ephemeral-restart-results start-vms vmiLatency vmReadyLatency
  check_quantile_recorded ./virt-ephemeral-restart-results start-vms dvLatency Ready
  check_quantile_recorded ./virt-ephemeral-restart-results start-vms vmiLatency VMReady
}
