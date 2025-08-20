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
    local error_output
    local temp_error_file
    temp_error_file=$(mktemp)

    output=$(virtctl ssh ${LOCAL_SSH} --local-ssh-opts="-o StrictHostKeyChecking=no"  --local-ssh-opts="-o UserKnownHostsFile=/dev/null" -n "${namespace}" -i "${identity_file}" -c "${command}" --username "${remote_user}"  vm/"${vm_name}" 2>"${temp_error_file}")
    local ret=$?

    error_output=$(cat "${temp_error_file}")
    rm -f "${temp_error_file}"

    if [ $ret -ne 0 ]; then
        if [ -n "${error_output}" ]; then
            echo "${error_output}" >&2
        fi
        return 1
    fi
    echo "${output}"
}

check_vm_running() {
    local vm=$1
    local output
    local error_output
    local temp_error_file
    temp_error_file=$(mktemp)

    output=$(remote_command "${NAMESPACE}" "${IDENTITY_FILE}" "${REMOTE_USER}" "${vm}" "ls" 2>"${temp_error_file}")
    local ret=$?

    error_output=$(cat "${temp_error_file}")
    rm -f "${temp_error_file}"

    if [ $ret -eq 0 ]; then
        echo "${output}"
    elif [ -n "${error_output}" ]; then
            echo "${error_output}" >&2
    fi
    return $ret
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

main() {
    local VMS
    VMS=$(get_vms "${NAMESPACE}" "${LABEL_KEY}" "${LABEL_VALUE}")

    for vm in ${VMS}; do
        for attempt in $(seq 1 $MAX_RETRIES); do
            local command_error
            local temp_error_file
            temp_error_file=$(mktemp)

            ${COMMAND} "${vm}" 2>"${temp_error_file}"
            local ret=$?

            command_error=$(cat "${temp_error_file}")
            rm -f "${temp_error_file}"

            if [ $ret -eq 0 ]; then
                break
            fi
            if [ "${attempt}" -lt $MAX_RETRIES ]; then
                if [ "${attempt}" -lt $MAX_SHORT_WAITS ]; then
                    sleep "${SHORT_WAIT}"
                else
                    sleep "${LONG_WAIT}"
                fi
            else
                if [ -n "${command_error}" ]; then
                    echo "Failed waiting on ${COMMAND} for ${vm}: ${command_error}" >&2
                else
                    echo "Failed waiting on ${COMMAND} for ${vm}" >&2
                fi
                exit 1
            fi
        done
        echo "${COMMAND} finished successfully for ${vm}"
    done
}

main
