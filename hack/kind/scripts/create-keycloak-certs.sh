#!/usr/bin/env bash
# Generate Keycloak TLS certificates using mkcert and create Kubernetes secrets.
# mkcert's root CA is already trusted by the host OS, so MCP clients can reach
# Keycloak's OIDC endpoints without extra trust configuration.
set -euo pipefail

KEYCLOAK_NS="${KEYCLOAK_NS:-keycloak}"
KCP_NS="${KCP_NS:-kcp-system}"
CERT_DIR=$(mktemp -d)
trap 'rm -rf "$CERT_DIR"' EXIT

# Ensure mkcert is available
if ! command -v mkcert &>/dev/null; then
  echo "ERROR: mkcert is required but not installed. Install with: brew install mkcert" >&2
  exit 1
fi

# Install mkcert root CA if not already (idempotent)
mkcert -install 2>/dev/null || true

# Generate cert for all Keycloak hostnames
mkcert -cert-file "$CERT_DIR/tls.crt" -key-file "$CERT_DIR/tls.key" \
  keycloak.kcp.example \
  keycloak-keycloakx-http \
  keycloak-keycloakx-http.keycloak \
  keycloak-keycloakx-http.keycloak.svc.cluster.local

# Create the keycloak-tls secret (used by Keycloak pod)
kubectl create namespace "$KEYCLOAK_NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret tls keycloak-tls \
  --cert="$CERT_DIR/tls.crt" \
  --key="$CERT_DIR/tls.key" \
  -n "$KEYCLOAK_NS" \
  --dry-run=client -o yaml | kubectl apply -f -

# Create keycloak-ca secret with the mkcert root CA (used by kcp for OIDC verification)
MKCERT_CA="$(mkcert -CAROOT)/rootCA.pem"
kubectl create namespace "$KCP_NS" --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic keycloak-ca \
  -n "$KEYCLOAK_NS" \
  --from-file=ca.crt="$MKCERT_CA" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic keycloak-ca \
  -n "$KCP_NS" \
  --from-file=ca.crt="$MKCERT_CA" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "==> Keycloak TLS secrets created (mkcert — trusted by host OS)"
