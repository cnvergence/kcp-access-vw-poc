#!/usr/bin/env bash
# Sets up a Kind cluster with kcp + Envoy AI Gateway + Keycloak + access-vw + kubernetes-mcp-server.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-kcp-access-vw}"

# Versions
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.17.2}"
ENVOY_GATEWAY_VERSION="${ENVOY_GATEWAY_VERSION:-v1.4.0}"
KCP_OPERATOR_REF="${KCP_OPERATOR_REF:-main}"

# Namespaces
KCP_NS="kcp-system"
KEYCLOAK_NS="keycloak"
ACCESS_VW_NS="kcp-system"
MCP_NS="default"

info()  { echo "==> $*"; }
warn()  { echo "⚠️  $*" >&2; }
error() { echo "❌ $*" >&2; exit 1; }

wait_for_pods() {
  local ns="$1" label="$2" timeout="${3:-300s}"
  info "Waiting for pods ${label} in ${ns}..."
  kubectl wait --for=condition=Ready pods -l "${label}" -n "${ns}" --timeout="${timeout}" 2>/dev/null || true
}

wait_for_deployment() {
  local ns="$1" name="$2" timeout="${3:-300s}"
  info "Waiting for deployment ${name} in ${ns}..."
  kubectl rollout status deployment/"${name}" -n "${ns}" --timeout="${timeout}"
}

wait_for_crd() {
  local crd="$1" timeout="${2:-120}"
  info "Waiting for CRD ${crd}..."
  local i=0
  while ! kubectl get crd "${crd}" &>/dev/null; do
    sleep 2
    i=$((i + 2))
    if [ "${i}" -ge "${timeout}" ]; then
      error "Timed out waiting for CRD ${crd}"
    fi
  done
}

# ─── Step 1: Kind cluster ────────────────────────────────────────────────────

step_kind_cluster() {
  info "Step 1: Creating Kind cluster '${CLUSTER_NAME}'..."

  if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    warn "Cluster '${CLUSTER_NAME}' already exists. Delete with: kind delete cluster --name ${CLUSTER_NAME}"
    return 0
  fi

  kind create cluster --name "${CLUSTER_NAME}" --config "${SCRIPT_DIR}/kind-config.yaml"
  kubectl cluster-info --context "kind-${CLUSTER_NAME}"
}

# ─── Step 2: cert-manager ────────────────────────────────────────────────────

step_cert_manager() {
  info "Step 2: Installing cert-manager ${CERT_MANAGER_VERSION}..."

  kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
  kubectl wait --for=condition=Available deployment/cert-manager -n cert-manager --timeout=120s
  kubectl wait --for=condition=Available deployment/cert-manager-webhook -n cert-manager --timeout=120s

  # Wait for webhook to be actually ready
  info "Waiting for cert-manager webhook to be ready..."
  sleep 10
}

# ─── Step 3: Envoy Gateway + AI Gateway ──────────────────────────────────────

step_envoy_gateway() {
  info "Step 3: Installing Envoy Gateway ${ENVOY_GATEWAY_VERSION}..."

  helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm \
    --version "${ENVOY_GATEWAY_VERSION}" \
    --namespace envoy-gateway-system \
    --create-namespace \
    -f "${SCRIPT_DIR}/manifests/envoy-gateway-values.yaml" \
    --wait --timeout 120s

  info "Installing Envoy AI Gateway CRDs..."
  helm upgrade --install ai-gateway-crds oci://docker.io/envoyproxy/ai-gateway-crds-helm \
    --version v0.5.1 \
    --namespace envoy-ai-gateway-system \
    --create-namespace \
    --wait --timeout 60s

  info "Installing Envoy AI Gateway controller..."
  helm upgrade --install ai-gateway oci://docker.io/envoyproxy/ai-gateway-helm \
    --version v0.5.1 \
    --namespace envoy-ai-gateway-system \
    --create-namespace \
    --wait --timeout 120s
}

# ─── Step 4: kcp ─────────────────────────────────────────────────────────────

step_kcp() {
  info "Step 4: Deploying kcp via kcp-operator..."

  # Deploy kcp-operator
  kubectl create namespace "${KCP_NS}" --dry-run=client -o yaml | kubectl apply -f -
  kubectl apply -k "https://github.com/kcp-dev/kcp-operator/config/default?ref=${KCP_OPERATOR_REF}"
  wait_for_deployment kcp-operator-system kcp-operator-controller-manager 120s

  # Create self-signed issuer for kcp certificates
  kubectl apply -f "${SCRIPT_DIR}/manifests/kcp/issuer.yaml"

  # Deploy etcd
  kubectl apply -f "${SCRIPT_DIR}/manifests/kcp/etcd.yaml"
  wait_for_deployment "${KCP_NS}" etcd 120s

  # Create RootShard
  wait_for_crd rootshards.operator.kcp.io
  kubectl apply -f "${SCRIPT_DIR}/manifests/kcp/root-shard.yaml"

  info "Waiting for RootShard to be ready..."
  kubectl wait --for=condition=Ready rootshard/root -n "${KCP_NS}" --timeout=300s || {
    warn "RootShard not ready yet, checking status..."
    kubectl get rootshard root -n "${KCP_NS}" -o yaml
  }

  # Create FrontProxy
  wait_for_crd frontproxies.operator.kcp.io
  kubectl apply -f "${SCRIPT_DIR}/manifests/kcp/front-proxy.yaml"

  info "Waiting for FrontProxy to be ready..."
  kubectl wait --for=condition=Ready frontproxy/frontproxy -n "${KCP_NS}" --timeout=300s || {
    warn "FrontProxy not ready yet, checking status..."
    kubectl get frontproxy frontproxy -n "${KCP_NS}" -o yaml
  }

  # Extract admin kubeconfig
  info "Extracting admin kubeconfig..."
  local kubeconfig_secret
  kubeconfig_secret=$(kubectl get kubeconfig -n "${KCP_NS}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
  if [ -n "${kubeconfig_secret}" ]; then
    kubectl get kubeconfig "${kubeconfig_secret}" -n "${KCP_NS}" -o jsonpath='{.status.kubeconfig}' > "${SCRIPT_DIR}/admin.kubeconfig"
    info "Admin kubeconfig saved to ${SCRIPT_DIR}/admin.kubeconfig"
  else
    warn "No Kubeconfig resource found yet — you may need to extract it manually"
  fi
}

# ─── Step 5: Keycloak ────────────────────────────────────────────────────────

step_keycloak() {
  info "Step 5: Deploying Keycloak..."

  kubectl create namespace "${KEYCLOAK_NS}" --dry-run=client -o yaml | kubectl apply -f -

  helm upgrade --install keycloak oci://registry-1.docker.io/bitnamicharts/keycloak \
    --namespace "${KEYCLOAK_NS}" \
    -f "${SCRIPT_DIR}/manifests/keycloak/values.yaml" \
    --wait --timeout 300s

  info "Configuring Keycloak OIDC clients..."
  "${SCRIPT_DIR}/scripts/configure-keycloak.sh"
}

# ─── Step 6: access-vw ──────────────────────────────────────────────────────

step_access_vw() {
  info "Step 6: Building and deploying access-vw..."

  # Build the binary and container image
  "${SCRIPT_DIR}/scripts/build-images.sh"

  # Deploy
  kubectl apply -f "${SCRIPT_DIR}/manifests/access-vw/deployment.yaml"
  wait_for_deployment "${ACCESS_VW_NS}" access-vw 120s
}

# ─── Step 7: kubernetes-mcp-server ───────────────────────────────────────────

step_mcp_server() {
  info "Step 7: Deploying kubernetes-mcp-server..."

  helm upgrade --install kubernetes-mcp-server \
    oci://ghcr.io/containers/charts/kubernetes-mcp-server \
    --version 0.1.0 \
    --namespace "${MCP_NS}" \
    -f "${SCRIPT_DIR}/manifests/mcp-server/values.yaml" \
    --wait --timeout 120s
}

# ─── Step 8: MCPRoute ────────────────────────────────────────────────────────

step_mcp_route() {
  info "Step 8: Applying MCPRoute..."
  kubectl apply -f "${SCRIPT_DIR}/manifests/mcp-route.yaml"
}

# ─── Step 9: Seed test data ─────────────────────────────────────────────────

step_seed() {
  info "Step 9: Seeding test workspace and RBAC..."

  if [ ! -f "${SCRIPT_DIR}/admin.kubeconfig" ]; then
    error "admin.kubeconfig not found — kcp may not be ready"
  fi

  export KUBECONFIG="${SCRIPT_DIR}/admin.kubeconfig"

  # Install APIExport
  kubectl apply -f "${REPO_ROOT}/config/apiexport/"

  # Create test workspace
  kubectl ws create test-workspace --type universal --enter || true
  kubectl apply -f "${REPO_ROOT}/config/examples/apibinding-consumer.yaml"
  kubectl ws use ':root'

  # Seed RBAC
  kubectl apply -f "${REPO_ROOT}/hack/seed-rbac.yaml" --context "$(kubectl config current-context)"

  info "Test data seeded. Verifying SCAR..."
  sleep 5

  # Quick SCAR test via trusted headers
  local scar_url
  scar_url="https://localhost:8443/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews"
  curl -sk -X POST -H 'X-Remote-User: alice' "${scar_url}" | jq . || warn "SCAR verification failed — check access-vw logs"
}

# ─── Main ────────────────────────────────────────────────────────────────────

main() {
  info "Setting up kcp-access-vw Kind environment"
  info ""

  step_kind_cluster
  step_cert_manager
  step_envoy_gateway
  step_kcp
  step_keycloak
  step_access_vw
  step_mcp_server
  step_mcp_route
  step_seed

  info ""
  info "✅ Setup complete!"
  info ""
  info "MCP endpoint:  https://localhost:8443/mcp"
  info "Keycloak:      kubectl port-forward -n ${KEYCLOAK_NS} svc/keycloak 8080:80"
  info "kcp admin:     export KUBECONFIG=${SCRIPT_DIR}/admin.kubeconfig"
  info "Debug graph:   kubectl port-forward -n ${ACCESS_VW_NS} svc/access-vw 9099:9099"
  info ""
  info "To connect Claude Code, add to .mcp.json:"
  info '  { "mcpServers": { "kcp": { "type": "http", "url": "https://localhost:8443/mcp" } } }'
}

main "$@"
