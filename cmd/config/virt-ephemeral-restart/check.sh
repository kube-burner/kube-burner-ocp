#!/usr/bin/env bash
COMMAND=$1
LABEL_KEY=$2
LABEL_VALUE=$3
NAMESPACE=$4
IDENTITY_FILE=$5
REMOTE_USER=$6
EXPECTED_ROOT_SIZE=$7
EXPECTED_DATA_SIZE=$8

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

    local vms
    vms=$(kubectl get vm -n "${namespace}" -l "${label_key}"="${label_value}" -o json | jq .items | jq -r '.[] | .metadata.name')
    local ret=$?
    if [ $ret -ne 0 ]; then
        echo "Failed to get VM list"
        exit 1
    fi
    echo "${vms}"
}

remote_command() {
    local namespace=$1
    local identity_file=$2
    local remote_user=$3
    local vm_name=$4
    local command=$5

    local output
    output=$(virtctl ssh ${LOCAL_SSH} --local-ssh-opts="-o StrictHostKeyChecking=no"  --local-ssh-opts="-o UserKnownHostsFile=/dev/null" -n "${namespace}" -i "${identity_file}" -c "${command}" --username "${remote_user}"  "${vm_name}" 2>/dev/null)
    local ret=$?
    if [ $ret -ne 0 ]; then
        return 1
    fi
    echo "${output}"
}

check_vm_running() {
    local vm=$1
    remote_command "${NAMESPACE}" "${IDENTITY_FILE}" "${REMOTE_USER}" "${vm}" "ls"
    return $?
}

check_resize() {
    local vm=$1

    local blk_devices
    blk_devices=$(remote_command "${NAMESPACE}" "${IDENTITY_FILE}" "${REMOTE_USER}" "${vm}" "lsblk --json -v --output=NAME,SIZE")
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
    datavolume_sizes=$(echo "${blk_devices}" | jq .blockdevices | jq -r --arg name "vda" '.[] | select(.name != $name) | .size')
    for datavolume_size in ${datavolume_sizes}; do
        if [[ $datavolume_size != "${EXPECTED_DATA_SIZE}" ]]; then
            return 1
        fi
    done

    return 0
}

VMS=$(get_vms "${NAMESPACE}" "${LABEL_KEY}" "${LABEL_VALUE}")

for vm in ${VMS}; do
    for attempt in $(seq 1 $MAX_RETRIES); do
        if ${COMMAND} "${vm}"; then
            break
        fi
        if [ "${attempt}" -lt $MAX_RETRIES ]; then
            if [ "${attempt}" -lt $MAX_SHORT_WAITS ]; then
                sleep "${SHORT_WAIT}"
            else
                sleep "${LONG_WAIT}"
            fi
        else
            echo "Failed waiting on ${COMMAND} for ${vm}" >&2
            exit 1
        fi
    done
    echo "${COMMAND} finished successfully for ${vm}"
done
