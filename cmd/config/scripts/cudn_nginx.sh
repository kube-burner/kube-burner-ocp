#!/bin/bash

set -euo pipefail

ACTION=${1:-""}
HTTP_SERVER_ADDRESS=${2:-""}
CONTAINER_NAME="cudn-nginx"

cleanup() {
  echo "Cleaning up nginx HTTP server..."
  podman rm -f -t 0 "$CONTAINER_NAME" 2>/dev/null || true
  echo "nginx HTTP server cleaned up"
}

case "$ACTION" in
  setup)
    cleanup
    if [[ -z "$HTTP_SERVER_ADDRESS" ]]; then
      echo "ERROR: HTTP_SERVER_ADDRESS (ip:port) is required as second argument"
      exit 1
    fi
    echo "Setting up nginx HTTP server on ${HTTP_SERVER_ADDRESS}..."
    podman run -d --name "$CONTAINER_NAME" -p "${HTTP_SERVER_ADDRESS}:8080" quay.io/cloud-bulldozer/nginx:latest
    echo "nginx HTTP server is running on ${HTTP_SERVER_ADDRESS}"
    ;;
  cleanup)
    cleanup
    ;;
  *)
    echo "Usage: $0 {setup|cleanup} [ip:port]"
    exit 1
    ;;
esac
