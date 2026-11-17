#!/bin/sh
#
# Runs the enmaas-loadtest workload. kube-burner-ocp picks up the pod's
# ServiceAccount token on its own, so there is nothing to wire up here.
#
#   args: ["--url=https://...", "--levels=100,200"]      -> enmaas-loadtest
#   args: ["version"]                                     -> passed through
set -eu

# Arguments are the workload's flags, so prepend the subcommand. A bare word
# first (no leading dash) means the caller wants a different subcommand.
if [ "$#" -eq 0 ] || [ "${1#-}" != "$1" ]; then
  set -- enmaas-loadtest "$@"
fi

if [ ! -s /var/run/secrets/kubernetes.io/serviceaccount/token ]; then
  echo "warning: no ServiceAccount token; set serviceAccountName and leave" >&2
  echo "         automountServiceAccountToken enabled to reach the cluster." >&2
fi

exec kube-burner-ocp "$@"
