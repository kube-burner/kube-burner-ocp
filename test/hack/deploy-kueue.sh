#!/bin/bash

oc apply -f kueue/manifests.yml
oc apply -f kueue/kueue.yml
echo "Waiting for Kueue Controller Manager..."
timeout --foreground 300s bash -c 'until oc rollout status deployment kueue-controller-manager -n openshift-kueue-operator; do sleep 10; echo "Still waiting..."; done'
echo "Kueue Controller Manager is ready"
