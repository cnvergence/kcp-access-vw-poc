#!/usr/bin/env bash
# Builds container images and loads them into the Kind cluster.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-kcp-access-vw}"
CONTAINER_RUNTIME="${CONTAINER_RUNTIME:-docker}"

info() { echo "  [build] $*"; }

# Build access-vw binary
info "Building access-vw binary (linux/amd64)..."
cd "${REPO_ROOT}"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/access-vw-linux ./cmd/server/

# Build container image
info "Building access-vw container image..."
${CONTAINER_RUNTIME} build -t localhost/access-vw:local -f - "${REPO_ROOT}" <<'DOCKERFILE'
FROM gcr.io/distroless/static:nonroot
COPY bin/access-vw-linux /access-vw
USER 65532:65532
ENTRYPOINT ["/access-vw"]
DOCKERFILE

# Load into Kind
info "Loading access-vw image into Kind cluster '${CLUSTER_NAME}'..."
kind load docker-image localhost/access-vw:local --name "${CLUSTER_NAME}"

# Clean up
rm -f "${REPO_ROOT}/bin/access-vw-linux"

info "Done."
