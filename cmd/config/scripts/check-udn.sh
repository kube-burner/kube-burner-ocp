#!/usr/bin/env bash

set -e
set -x

# Enable persistent logging and command tracing
LOG_FILE="${LOG_FILE:-virt-udn-ssh-check-$(date +%Y%m%d-%H%M%S).log}"
exec > >(tee -a "$LOG_FILE") 2>&1

LABEL_KEY=$1
LABEL_VALUE=$2
NAMESPACE=$3
IDENTITY_FILE=$4
REMOTE_USER=$5
NAMESPACE_LABEL="${6:-}"

# Wait up to ~60 minutes
MAX_RETRIES=30
# In the first reties use a shorter sleep
MAX_SHORT_WAITS=12
SHORT_WAIT=5
LONG_WAIT=30
SSH_SERVICE=direct-ssh

# Global state populated/consumed across the functions below.
declare -A NS_PODS
declare -A NS_FIRST_POD
declare -A SSH_HOST_IP
declare -A SSH_NODE_PORT
CREATED_NAMESPACES=()


# Agent forwarding (-A) is used so that the bastion VM can authenticate to
# the other VMs on the private UDN network without the private key ever
# being copied there. That requires a running ssh-agent with the key
# loaded locally; start a dedicated one for this script rather than
# relying on one already being present in the environment (this is what
# was causing "Could not open a connection to your authentication agent"
# and the subsequent fallback to password auth).
setup_ssh_agent() {
    eval "$(ssh-agent -s)" >/dev/null
    if ! ssh-add "${IDENTITY_FILE}"; then
        echo "Failed to add ${IDENTITY_FILE} to the ssh-agent" >&2
        exit 1
    fi
}

get_pods_virt_runner() {
    local namespace=$1
    local label_key=$2
    local label_value=$3
    local namespace_label=$4

    local virt_runner_pods=""
    if [ -n "${namespace_label}" ]; then
        # Get namespaces matching the label selector
        local namespaces
        if ! namespaces=$(kubectl get namespace -l "${namespace_label}" -o jsonpath='{.items[*].metadata.name}'); then
            echo "Failed to get namespaces with label ${namespace_label}" >&2
            exit 1
        fi
        if [ -z "${namespaces}" ]; then
            echo "No namespaces found matching label: ${namespace_label}" >&2
            return 0
        fi

        # Get virt-launcher pods from each namespace
        # Output format: "namespace/podname"
        for ns in ${namespaces}; do
            local ns_pods
            if ! ns_pods=$(kubectl get pod -n "${ns}" -l "${label_key}"="${label_value}" --field-selector=status.phase==Running -o jsonpath='{.items[*].metadata.name}'); then
                echo "Failed to get pods in namespace ${ns}" >&2
                exit 1
            fi
            for pod in ${ns_pods}; do
                virt_runner_pods="${virt_runner_pods}${ns}/${pod}"$'\n'
            done
        done
    else
        # Single namespace mode 
        if ! virt_runner_pods=$(kubectl get pod -n "${namespace}" -l "${label_key}"="${label_value}" --field-selector=status.phase==Running -o json | jq -r '.items[] | .metadata.name'); then
            echo "Failed to get pod list" >&2
            exit 1
        fi
    fi
    echo "${virt_runner_pods}"
}

# Group the pods returned by get_pods_virt_runner into NS_PODS (namespace ->
# newline-separated pod names) and NS_FIRST_POD (namespace -> first pod seen,
# used later as the SSH bastion for that namespace).
group_pods_by_namespace() {
    local virt_pods
    virt_pods=$(get_pods_virt_runner "${NAMESPACE}" "${LABEL_KEY}" "${LABEL_VALUE}" "${NAMESPACE_LABEL}")

    local pod_entry pod_namespace pod_name
    while IFS= read -r pod_entry; do
        [ -z "${pod_entry}" ] && continue

        if [[ "${pod_entry}" == *"/"* ]]; then
            pod_namespace="${pod_entry%/*}"
            pod_name="${pod_entry#*/}"
        else
            pod_namespace="${NAMESPACE}"
            pod_name="${pod_entry}"
        fi

        if [ -z "${NS_FIRST_POD[${pod_namespace}]:-}" ]; then
            NS_FIRST_POD[${pod_namespace}]="${pod_name}"
        fi
        NS_PODS[${pod_namespace}]+="${pod_name}"$'\n'
    done <<< "${virt_pods}"

    if [ ${#NS_PODS[@]} -eq 0 ]; then
        echo "No virt-handler pods found" >&2
        exit 1
    fi
}

#Create a nodeport service for the ssh service and label the virt-runner pod with the app=${SSH_SERVICE} label
set_up_ssh() {
    local identity_file=$1
    local remote_user=$2
    local namespace=$3
    local virt_runner_pod_name=$4

    local host_ip
    local node_port

    kubectl apply -f <(kubectl create svc nodeport ${SSH_SERVICE} --tcp=22 -o yaml --dry-run=client) -n "${namespace}" >/dev/null 2>&1
    node_port=$(kubectl get svc ${SSH_SERVICE} -n "${namespace}" -o jsonpath='{.spec.ports[0].nodePort}')
    kubectl label pod "${virt_runner_pod_name}" -n "${namespace}" app=${SSH_SERVICE} --overwrite  >/dev/null 2>&1
    host_ip=$(kubectl get pod "${virt_runner_pod_name}" -n "${namespace}" -o jsonpath='{.status.hostIP}')
    ssh -A -i "${identity_file}" "${remote_user}@${host_ip}" -p "${node_port}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ls >/dev/null 2>&1
    echo "${host_ip} ${node_port}"
}

# Set up one SSH nodeport service per namespace, using the first pod found in
# that namespace as the bastion.
setup_bastions() {
    local ns ssh_info ns_host_ip ns_node_port
    for ns in "${!NS_PODS[@]}"; do
        ssh_info=$(set_up_ssh "${IDENTITY_FILE}" "${REMOTE_USER}" "${ns}" "${NS_FIRST_POD[${ns}]}")
        read -r ns_host_ip ns_node_port <<< "${ssh_info}"
        SSH_HOST_IP[${ns}]="${ns_host_ip}"
        SSH_NODE_PORT[${ns}]="${ns_node_port}"
        CREATED_NAMESPACES+=("${ns}")
    done
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


# Cleanup every nodeport service we create, and the ssh-agent we started,
# even on early exit/failure.
cleanup() {
    local ns
    for ns in "${CREATED_NAMESPACES[@]}"; do
        kubectl delete svc "${SSH_SERVICE}" -n "${ns}" --ignore-not-found >/dev/null 2>&1
    done
    if [ -n "${SSH_AGENT_PID:-}" ]; then
        ssh-agent -k >/dev/null 2>&1
    fi
}

main() {
    if [ -n "${NAMESPACE}" ] && [ -n "${NAMESPACE_LABEL}" ]; then
        echo "Error: Cannot specify both namespace and namespace label selector. Use one or the other." >&2
        exit 1
    fi

    if [ -z "${NAMESPACE}" ] && [ -z "${NAMESPACE_LABEL}" ]; then
        echo "Error: Must specify either namespace or namespace label selector" >&2
        exit 1
    fi
    setup_ssh_agent
    group_pods_by_namespace
    trap cleanup EXIT
    setup_bastions
    
    #check the UDN connectivity
    local ns host_ip node_port pod pod_networks pod_ip COMMAND attempt
    for ns in "${!NS_PODS[@]}"; do
        host_ip="${SSH_HOST_IP[${ns}]}"
        node_port="${SSH_NODE_PORT[${ns}]}"
        while IFS= read -r pod; do
            [ -z "${pod}" ] && continue

            pod_networks=$(kubectl get pod "${pod}" -n "${ns}" -o jsonpath='{.metadata.annotations.k8s\.ovn\.org/pod-networks}')
            pod_ip=$(echo "${pod_networks}" | jq -r '[to_entries[] | select(.key != "default")] | if length == 0 then empty else .[0].value.ip_address | split("/")[0] end')
            if [ -z "${pod_ip}" ]; then
                echo "Failed to determine UDN pod IP for ${ns}/${pod} from pod-networks annotation: ${pod_networks}" >&2
                exit 1
            fi
            COMMAND="ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ${pod_ip} ls"
            for attempt in $(seq 1 $MAX_RETRIES); do
                echo "Attempt ${attempt} of ${MAX_RETRIES} to run remote_command ${COMMAND} for ${ns}/${pod}"
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
                    echo "Failed waiting on remote_command ${COMMAND} for ${ns}/${pod}" >&2
                    exit 1
                fi
            done
            echo "${COMMAND} finished successfully for ${ns}/${pod}"
        done <<< "${NS_PODS[${ns}]}"
    done
}

main
