#!/usr/bin/env bash
# Tears down the Kind cluster created by setup.sh.
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-kcp-access-vw}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "==> Deleting Kind cluster '${CLUSTER_NAME}'..."
kind delete cluster --name "${CLUSTER_NAME}" 2>/dev/null || true

# Clean up generated files
rm -f "${SCRIPT_DIR}/admin.kubeconfig"

echo "==> Done."
