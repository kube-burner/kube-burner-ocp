#!/usr/bin/env bash

# Enable persistent logging and command tracing
LOG_FILE="${LOG_FILE:-virt-density-ssh-check-$(date +%Y%m%d-%H%M%S).log}"
exec > >(tee -a "$LOG_FILE") 2>&1

COMMAND=$1
LABEL_KEY=$2
LABEL_VALUE=$3
NAMESPACE=$4
IDENTITY_FILE=$5
REMOTE_USER=$6
EXPECTED_ROOT_SIZE=$7
EXPECTED_DATA_SIZE=$8
NAMESPACE_LABEL="${9:-}"

if [ -n "${NAMESPACE}" ] && [ -n "${NAMESPACE_LABEL}" ]; then
    echo "Error: Cannot specify both namespace and namespace label selector. Use one or the other." >&2
    exit 1
fi

if [ -z "${NAMESPACE}" ] && [ -z "${NAMESPACE_LABEL}" ]; then
    echo "Error: Must specify either namespace or namespace label selector" >&2
    exit 1
fi

# Wait up to ~60 minutes
MAX_RETRIES=130
# In the first reties use a shorter sleep
MAX_SHORT_WAITS=12
SHORT_WAIT=5
LONG_WAIT=30

if virtctl ssh --help | grep -qc "\--local-ssh " ; then
    LOCAL_SSH="--local-ssh"
else
    LOCAL_SSH=""
fi

get_vms() {
    local namespace=$1
    local label_key=$2
    local label_value=$3
    local namespace_label=$4

    local vms=""
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

        # Get VMs from each namespace
        # Output format: "namespace/vmname"
        for ns in ${namespaces}; do
            local ns_vms
            if ! ns_vms=$(kubectl get vm -n "${ns}" -l "${label_key}"="${label_value}" -o jsonpath='{.items[*].metadata.name}'); then
                echo "Failed to get VMs in namespace ${ns}" >&2
                exit 1
            fi
            for vm in ${ns_vms}; do
                vms="${vms}${ns}/${vm}"$'\n'
            done
        done
    else
        # Single namespace mode (backward compatible)
        if ! vms=$(kubectl get vm -n "${namespace}" -l "${label_key}"="${label_value}" -o json | jq -r '.items[] | .metadata.name'); then
            echo "Failed to get VM list" >&2
            exit 1
        fi
    fi
    echo "${vms}"
}

remote_command() {
    local namespace=$1
    local identity_file=$2
    local remote_user=$3
    local vm_name=$4
    local command=$5

    virtctl ssh ${LOCAL_SSH} --local-ssh-opts="-o StrictHostKeyChecking=no"  --local-ssh-opts="-o UserKnownHostsFile=/dev/null" -n "${namespace}" -i "${identity_file}" -c "${command}" --username "${remote_user}"  vm/"${vm_name}" </dev/null
}

check_vm_running() {
    local vm_namespace=$1
    local vm_name=$2

    remote_command "${vm_namespace}" "${IDENTITY_FILE}" "${REMOTE_USER}" "${vm_name}" "ls"
}

check_resize() {
    local vm_namespace=$1
    local vm_name=$2

    local blk_devices
    blk_devices=$(remote_command "${vm_namespace}" "${IDENTITY_FILE}" "${REMOTE_USER}" "${vm_name}" "lsblk --json -v --output=NAME,SIZE")
    local ret=$?
    if [ $ret -ne 0 ]; then
        return $ret
    fi

    local size
    size=$(echo "${blk_devices}" | jq .blockdevices | jq -r --arg name "vda" '.[] | select(.name == $name) | .size')
    if [[ $size != "${EXPECTED_ROOT_SIZE}" ]]; then
        return 1
    fi

    local datavolume_sizes
    datavolume_sizes=$(echo "${blk_devices}" | jq -r --arg name "vda" '.blockdevices[] | select(.name != $name and .size != "1M") | .size')
    for datavolume_size in ${datavolume_sizes}; do
        if [[ $datavolume_size != "${EXPECTED_DATA_SIZE}" ]]; then
            return 1
        fi
    done

    return 0
}

main() {
    local VMS
    VMS=$(get_vms "${NAMESPACE}" "${LABEL_KEY}" "${LABEL_VALUE}" "${NAMESPACE_LABEL}")

    # Process VMs
    while IFS= read -r vm_entry; do
        [ -z "${vm_entry}" ] && continue

        # Parse namespace and VM name
        if [[ $vm_entry == *"/"* ]]; then
            # Format is "namespace/vmname" (from namespace label mode)
            vm_namespace="${vm_entry%/*}"
            vm_name="${vm_entry#*/}"
        else
            # Format is just "vmname" (from single namespace mode)
            vm_namespace="${NAMESPACE}"
            vm_name="${vm_entry}"
        fi

        for attempt in $(seq 1 $MAX_RETRIES); do
            if ${COMMAND} "${vm_namespace}" "${vm_name}"; then
                break
            fi

            if [ "${attempt}" -lt $MAX_RETRIES ]; then
                if [ "${attempt}" -lt $MAX_SHORT_WAITS ]; then
                    sleep "${SHORT_WAIT}"
                else
                    sleep "${LONG_WAIT}"
                fi
            else
                echo "Failed waiting on ${COMMAND} for ${vm_namespace}/${vm_name}" >&2
                exit 1
            fi
        done
        echo "${COMMAND} finished successfully for ${vm_namespace}/${vm_name}"
    done <<< "${VMS}"
}

main
