#!/usr/bin/env bash
LABEL_KEY=$1
LABEL_VALUE=$2
NAMESPACE=$3
IDENTITY_FILE=$4
REMOTE_USER=$5

# Wait up to ~60 minutes
MAX_RETRIES=130
# In the first reties use a shorter sleep
MAX_SHORT_WAITS=12
SHORT_WAIT=5
LONG_WAIT=30
SSH_SERVICE=direct-ssh


get_pods_virt_handler() {
    local namespace=$1
    local label_key=$2
    local label_value=$3

    local virt_handler_pods

    virt_handler_pods=$(kubectl get pod -n "${namespace}" -l "${label_key}"="${label_value}" --field-selector=status.phase==Running -o json | jq .items | jq -r '.[] | .metadata.name')
    local ret=$?
    if [ $ret -ne 0 ]; then
        echo "Failed to get pod list"
        exit 1
    fi
    echo "${virt_handler_pods}"
}

set_up_ssh() {
    local identity_file=$1
    local remote_user=$2
    local namespace=$3
    local virt_handler_pod_name=$4

    local host_ip
    local node_port
    #ssh agent for remote command
    ssh-add -q "${identity_file}"
    #Create a nodeport service for the ssh
    kubectl apply -f <(kubectl create svc nodeport ${SSH_SERVICE} --tcp=22 -o yaml --dry-run=client) -n "${namespace}" >/dev/null 2>&1
    node_port=$(kubectl get svc ${SSH_SERVICE} -n "${namespace}" -o jsonpath='{.spec.ports[0].nodePort}')
    kubectl label pod "${virt_handler_pod_name}" -n "${namespace}" app=${SSH_SERVICE} --overwrite  >/dev/null 2>&1
    host_ip=$(kubectl get pod "${virt_handler_pod_name}" -n "${namespace}" -o jsonpath='{.status.hostIP}')
    ssh -A -i "${identity_file}" "${remote_user}"@"${host_ip}" -p "${node_port}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ls >/dev/null 2>&1
    echo "${host_ip} ${node_port}"
}

remote_command() {
    local identity_file=$1
    local remote_user=$2
    local host_ip=$3
    local node_port=$4
    local command=$5

    local output
    output=$(ssh "${remote_user}"@"${host_ip}" -p "${node_port}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -A  -i "${identity_file}" "${command}")
    local ret=$?
    if [ $ret -ne 0 ]; then
        return 1
    fi
    echo "${output}"
}

VIRT_PODS=$(get_pods_virt_handler "${NAMESPACE}" "${LABEL_KEY}" "${LABEL_VALUE}")
read -r first_pod VIRT_PODS <<< "${VIRT_PODS}"
SSH_INFO=$(set_up_ssh "${IDENTITY_FILE}" "${REMOTE_USER}" "${NAMESPACE}" "${first_pod}")
read -r host_ip node_port <<< "${SSH_INFO}"

for pod in ${VIRT_PODS}; do
    pod_ip=$(kubectl get pod "${pod}" -n "${NAMESPACE}" -o jsonpath='{.metadata.annotations.k8s\.ovn\.org/pod-networks}'| jq -r '."virt-migration/l2-network-migration".ip_address |split("/")[0]')
    COMMAND="ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ${pod_ip} ls"
    for attempt in $(seq 1 $MAX_RETRIES); do
        if remote_command "${IDENTITY_FILE}" "${REMOTE_USER}" "${host_ip}" "${node_port}" "${COMMAND}"; then
            break
        fi
        if [ "${attempt}" -lt $MAX_RETRIES ]; then
            if [ "${attempt}" -lt $MAX_SHORT_WAITS ]; then
                sleep "${SHORT_WAIT}"
            else
                sleep "${LONG_WAIT}"
            fi
        else
            echo "Failed waiting on remote_command ${COMMAND} for ${pod}" >&2
            exit 1
        fi
    done
    echo "${COMMAND} finished successfully for ${pod}"
done
#Cleanup
kubectl delete svc ${SSH_SERVICE} -n "${NAMESPACE}"