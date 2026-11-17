# Copyright 2026 The Kube-burner Authors.
# SPDX-License-Identifier: Apache-2.0
# Preserve pre-existing mock fixtures across the native agentic BATS test.

agentic_fixture_backup() {
  AGENTIC_FIXTURE_BACKUP=$(mktemp -d "${TMPDIR:-/tmp}/agentic-fixture.XXXXXX")
  chmod 700 "${AGENTIC_FIXTURE_BACKUP}"
  local kind name scope path
  while read -r kind name scope; do
    path="${AGENTIC_FIXTURE_BACKUP}/${kind}-${name}.json"
    oc get "${kind}" "${name}" ${scope:+-n "$scope"} --ignore-not-found -o json > "${path}" || return 1
    chmod 600 "${path}"
    if [[ -s "${path}" ]]; then
      jq 'del(.status, .metadata.managedFields, .metadata.resourceVersion,
        .metadata.creationTimestamp, .metadata.generation)' "${path}" > "${path}.clean" || return 1
      mv "${path}.clean" "${path}"
      chmod 600 "${path}"
    fi
  done < <(agentic_fixture_objects)
  touch "${AGENTIC_FIXTURE_BACKUP}/ready"
}

agentic_fixture_objects() {
  cat <<'EOF'
configmap mock-llm-server openshift-lightspeed
secret mock-llm-credentials openshift-lightspeed
service mock-llm openshift-lightspeed
deployment mock-llm openshift-lightspeed
llmprovider.agentic.openshift.io mock-llm
agent.agentic.openshift.io default
approvalpolicy.agentic.openshift.io cluster
EOF
}

agentic_fixture_replace() {
  local kind=$1 name=$2 scope=$3 path=$4 attempt
  for attempt in 1 2 3; do
    oc get "${kind}" "${name}" ${scope:+-n "$scope"} -o json > "${path}.current" || return 1
    # Replace removes test-added fields that client-side apply can leave behind.
    # A different UID means another actor replaced the fixture; never overwrite it.
    jq --slurpfile current "${path}.current" '
      if .metadata.uid != $current[0].metadata.uid then
        error("Fixture identity changed during the test")
      else .metadata.resourceVersion = $current[0].metadata.resourceVersion end
    ' "${path}" > "${path}.restore" || return 1
    chmod 600 "${path}.current" "${path}.restore"
    if oc replace -f "${path}.restore"; then
      return 0
    fi
    echo "Fixture replacement attempt ${attempt} failed for ${kind}/${name}" >&2
  done
  return 1
}

agentic_fixture_restore() {
  [[ -n "${AGENTIC_FIXTURE_BACKUP:-}" && -f "${AGENTIC_FIXTURE_BACKUP}/ready" ]] || return 0
  local kind name scope path failed=0
  while read -r kind name scope; do
    path="${AGENTIC_FIXTURE_BACKUP}/${kind}-${name}.json"
    if [[ -s "${path}" ]]; then
      agentic_fixture_replace "${kind}" "${name}" "${scope}" "${path}" || failed=1
    else
      oc delete "${kind}" "${name}" ${scope:+-n "$scope"} --ignore-not-found || failed=1
    fi
  done < <(agentic_fixture_objects)
  if [[ -s "${AGENTIC_FIXTURE_BACKUP}/deployment-mock-llm.json" ]]; then
    oc rollout status -n openshift-lightspeed deployment/mock-llm --timeout=300s || failed=1
  fi
  if [[ "${failed}" == 0 ]]; then
    rm -rf "${AGENTIC_FIXTURE_BACKUP}"
  else
    echo "Mock fixture restoration failed; private snapshot retained at ${AGENTIC_FIXTURE_BACKUP}" >&2
  fi
  return "${failed}"
}
