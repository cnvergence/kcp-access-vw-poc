#!/usr/bin/env bash
# Sets up a Kind cluster with kcp + Envoy AI Gateway + Keycloak + access-vw + kubernetes-mcp-server.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-kcp-access-vw}"
KIND_KUBECONFIG="${HOME}/.kube/config"

# Versions
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.20.2}"
ENVOY_GATEWAY_VERSION="${ENVOY_GATEWAY_VERSION:-v1.8.0}"
METALLB_VERSION="${METALLB_VERSION:-v0.15.3}"
KCP_OPERATOR_REF="${KCP_OPERATOR_REF:-main}"
IP_FAMILY="${IP_FAMILY:-ipv4}"

# Namespaces
KCP_NS="kcp-system"
KEYCLOAK_NS="keycloak"
ACCESS_VW_NS="kcp-system"
MCP_NS="mcp"

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

# ─── Step 1b: MetalLB ─────────────────────────────────────────────────────────

step_metallb() {
  info "Step 1b: Installing MetalLB ${METALLB_VERSION}..."

  kubectl apply -f "https://raw.githubusercontent.com/metallb/metallb/${METALLB_VERSION}/config/manifests/metallb-native.yaml"

  wait_for_deployment "metallb-system" "controller" 120s

  # Create memberlist secret if it doesn't exist
  local needCreate
  needCreate="$(kubectl get secret -n metallb-system memberlist --no-headers --ignore-not-found -o custom-columns=NAME:.metadata.name)"
  if [ -z "$needCreate" ]; then
    kubectl create secret generic -n metallb-system memberlist --from-literal=secretkey="$(openssl rand -base64 128)"
  fi

  # Wait for MetalLB webhook to be ready before applying config
  info "Waiting for MetalLB webhooks..."
  sleep 5

  # Retry applying IPAddressPool — webhook may take a moment
  local attempt
  for attempt in 1 2 3 4 5; do
    if kubectl apply -f - <<EOF 2>/dev/null
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  namespace: metallb-system
  name: kube-services
spec:
  addresses:
    - 172.18.0.200-172.18.0.250
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: kube-services
  namespace: metallb-system
spec:
  ipAddressPools:
  - kube-services
EOF
    then
      info "MetalLB configured."
      return 0
    fi
    info "MetalLB webhook not ready yet, retrying (${attempt}/5)..."
    sleep 5
  done
  error "Failed to configure MetalLB IPAddressPool after retries"
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

# ─── Step 4a: Keycloak TLS certs ──────────────────────────────────────────────

step_keycloak_certs() {
  info "Step 4a: Creating Keycloak TLS certificate..."

  kubectl create namespace "${KEYCLOAK_NS}" --dry-run=client -o yaml | kubectl apply -f -

  # Deploy TLS certificate (kcp requires HTTPS OIDC issuer)
  kubectl apply -f "${SCRIPT_DIR}/manifests/keycloak/certificate.yaml"
  kubectl wait --for=condition=Ready certificate/keycloak-tls -n "${KEYCLOAK_NS}" --timeout=60s

  # Copy Keycloak CA cert to kcp-system so RootShard/FrontProxy can trust it
  info "Copying Keycloak CA to ${KCP_NS}..."
  kubectl create namespace "${KCP_NS}" --dry-run=client -o yaml | kubectl apply -f -
  kubectl get secret keycloak-ca -n "${KEYCLOAK_NS}" -o jsonpath='{.data.ca\.crt}' | \
    base64 -d > /tmp/keycloak-ca.crt
  kubectl create secret generic keycloak-ca -n "${KCP_NS}" \
    --from-file=ca.crt=/tmp/keycloak-ca.crt \
    --dry-run=client -o yaml | kubectl apply -f -
  rm -f /tmp/keycloak-ca.crt
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
    --version 0.6.0 \
    --namespace envoy-ai-gateway-system \
    --create-namespace \
    --wait --timeout 60s

  info "Installing Envoy AI Gateway controller..."
  helm upgrade --install ai-gateway oci://docker.io/envoyproxy/ai-gateway-helm \
    --version 0.6.0 \
    --namespace envoy-ai-gateway-system \
    --create-namespace \
    --wait --timeout 120s
}

# ─── Step 4: kcp ─────────────────────────────────────────────────────────────

step_kcp() {
  info "Step 4: Deploying kcp via kcp-operator..."

  # Deploy kcp-operator (local overlay pins image to :main)
  kubectl create namespace "${KCP_NS}" --dry-run=client -o yaml | kubectl apply -f -
  kubectl apply -k "${SCRIPT_DIR}/manifests/kcp-operator"
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
  kubectl wait --for=condition=Available rootshard/root -n "${KCP_NS}" --timeout=300s || {
    warn "RootShard not ready yet, checking status..."
    kubectl get rootshard root -n "${KCP_NS}" -o yaml
  }

  # Create FrontProxy
  wait_for_crd frontproxies.operator.kcp.io
  kubectl apply -f "${SCRIPT_DIR}/manifests/kcp/front-proxy.yaml"

  info "Waiting for FrontProxy to be ready..."
  kubectl wait --for=condition=Available frontproxy/frontproxy -n "${KCP_NS}" --timeout=300s || {
    warn "FrontProxy not ready yet, checking status..."
    kubectl get frontproxy frontproxy -n "${KCP_NS}" -o yaml
  }

  # Patch RootShard with hostAliases so kcp controllers can reach
  # root.kcp.example (external hostname) via the in-cluster front proxy.
  local fp_ip
  fp_ip=$(kubectl get svc frontproxy-front-proxy -n "${KCP_NS}" -o jsonpath='{.spec.clusterIP}')
  info "Patching RootShard hostAliases → ${fp_ip} root.kcp.example"
  kubectl patch rootshard root -n "${KCP_NS}" --type merge -p "
spec:
  deploymentTemplate:
    spec:
      template:
        spec:
          hostAliases:
            - ip: \"${fp_ip}\"
              hostnames:
                - root.kcp.example
"
  # Wait for the rollout triggered by the patch
  wait_for_deployment "${KCP_NS}" root-kcp 120s

  # Create a NodePort service to expose FrontProxy on the Kind host.
  # The kcp-operator manages the ClusterIP service, so we create a separate one.
  kubectl apply -f - <<-EOF
apiVersion: v1
kind: Service
metadata:
  name: frontproxy-nodeport
  namespace: ${KCP_NS}
spec:
  type: NodePort
  selector:
    app.kubernetes.io/component: front-proxy
    app.kubernetes.io/instance: frontproxy
    app.kubernetes.io/managed-by: kcp-operator
    app.kubernetes.io/name: kcp
  ports:
    - port: 8443
      targetPort: 6443
      nodePort: 30644
      protocol: TCP
EOF

  # Create admin Kubeconfig CR
  kubectl apply -f "${SCRIPT_DIR}/manifests/kcp/admin-kubeconfig.yaml"
  info "Waiting for admin Kubeconfig to be ready..."
  kubectl wait --for=jsonpath='{.status.phase}'=Ready kubeconfig/kcp-admin-kubeconfig \
    -n "${KCP_NS}" --timeout=120s

  # Extract admin kubeconfig to file for CLI use
  info "Extracting admin kubeconfig..."
  kubectl get secret kcp-admin-kubeconfig -n "${KCP_NS}" \
    -o jsonpath='{.data.kubeconfig}' | base64 -d > "${SCRIPT_DIR}/admin.kubeconfig"

  # Rewrite in-cluster URLs to use the Kind-exposed hostname
  # and skip TLS verify since the cert won't match the hostname
  local kc="${SCRIPT_DIR}/admin.kubeconfig"
  sed -i.bak 's|https://root-kcp.kcp-system.svc.cluster.local:6443|https://root.kcp.example:6443|g' "${kc}"
  rm -f "${kc}.bak"
  KUBECONFIG="${kc}" kubectl config set-cluster base --insecure-skip-tls-verify=true 2>/dev/null || true
  KUBECONFIG="${kc}" kubectl config set-cluster default --insecure-skip-tls-verify=true 2>/dev/null || true
  info "Admin kubeconfig saved to ${kc}"
}

# ─── Step 5: Keycloak ────────────────────────────────────────────────────────

step_keycloak() {
  info "Step 5: Deploying Keycloak..."

  # Namespace and TLS cert already created in step_keycloak_certs

  helm repo add codecentric https://codecentric.github.io/helm-charts 2>/dev/null || true
  helm repo update codecentric

  helm upgrade --install keycloak codecentric/keycloakx \
    --namespace "${KEYCLOAK_NS}" \
    -f "${SCRIPT_DIR}/manifests/keycloak/values.yaml" \
    --wait --timeout 300s

  info "Configuring Keycloak OIDC clients and users..."
  "${SCRIPT_DIR}/scripts/configure-keycloak.sh"

  # Restart kcp components so they can complete OIDC discovery.
  # FrontProxy and RootShard start before Keycloak (step 4 vs step 5) and cache
  # the OIDC init failure until restarted.
  info "Restarting kcp components to pick up OIDC discovery..."
  kubectl rollout restart deployment/root-kcp -n "${KCP_NS}"
  kubectl rollout restart deployment/frontproxy-front-proxy -n "${KCP_NS}"
  wait_for_deployment "${KCP_NS}" root-kcp 120s
  wait_for_deployment "${KCP_NS}" frontproxy-front-proxy 120s
}

# ─── Step 6: access-vw ──────────────────────────────────────────────────────

step_access_vw() {
  info "Step 6: Building and deploying access-vw..."

  # Build the binary and container image
  "${SCRIPT_DIR}/scripts/build-images.sh"

  # Create Kubeconfig CR — kcp-operator generates the secret
  kubectl apply -f "${SCRIPT_DIR}/manifests/access-vw/kubeconfig.yaml"
  info "Waiting for access-vw Kubeconfig to be ready..."
  kubectl wait --for=jsonpath='{.status.phase}'=Ready kubeconfig/access-vw-kubeconfig \
    -n "${KCP_NS}" --timeout=120s

  # Deploy
  kubectl apply -f "${SCRIPT_DIR}/manifests/access-vw/deployment.yaml"
  wait_for_deployment "${ACCESS_VW_NS}" access-vw 120s
}

# ─── Step 7: kubernetes-mcp-server ───────────────────────────────────────────

step_mcp_server() {
  info "Step 7: Deploying kubernetes-mcp-server..."

  kubectl create namespace "${MCP_NS}" --dry-run=client -o yaml | kubectl apply -f -

  # Create Kubeconfig CR for MCP server (in kcp-system where kcp-operator runs)
  kubectl apply -f "${SCRIPT_DIR}/manifests/mcp-server/kubeconfig.yaml"
  info "Waiting for mcp-server Kubeconfig to be ready..."
  kubectl wait --for=jsonpath='{.status.phase}'=Ready kubeconfig/mcp-server-kubeconfig \
    -n "${KCP_NS}" --timeout=120s

  # Copy the kubeconfig secret from kcp-system to the mcp namespace
  kubectl get secret mcp-server-kubeconfig -n "${KCP_NS}" -o json \
    | jq 'del(.metadata.namespace, .metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp, .metadata.ownerReferences)' \
    | kubectl apply -n "${MCP_NS}" -f -

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
  info "Step 9: Seeding user workspaces and RBAC..."

  if [ ! -f "${SCRIPT_DIR}/admin.kubeconfig" ]; then
    error "admin.kubeconfig not found — kcp may not be ready"
  fi

  # admin.kubeconfig points at root.kcp.example:6443 (exposed via Kind NodePort)
  export KUBECONFIG="${SCRIPT_DIR}/admin.kubeconfig"

  # Install APIExport
  kubectl apply -f "${REPO_ROOT}/config/apiexport/"

  # Create per-user workspaces with APIBinding + RBAC
  for user in alice bob; do
    local ws="${user}-workspace"
    kubectl ws create "${ws}" --type universal --enter 2>/dev/null || kubectl ws use "root:${ws}"

    # APIBinding lets access-vw discover RBAC in this workspace
    kubectl apply -f "${REPO_ROOT}/config/examples/apibinding-consumer.yaml"

    # Grant user cluster-admin in their own workspace
    kubectl apply -f "${REPO_ROOT}/config/rbac/seed-rbac-${user}.yaml"

    kubectl ws use ':root'
  done

  info "User workspaces seeded. Verifying SCAR..."
  sleep 5

  # Quick SCAR test — get an OIDC token for alice and call SCAR via FrontProxy
  # Both Keycloak (8443) and FrontProxy (6443) are exposed via Kind NodePort
  info "Getting OIDC token for alice..."
  local alice_token
  alice_token=$(curl -sf --insecure -X POST "https://keycloak.kcp.example:8443/realms/kcp/protocol/openid-connect/token" \
    -d "client_id=kcp" \
    -d "username=alice" \
    -d "password=alice" \
    -d "grant_type=password" \
    -d "scope=openid" | jq -r '.access_token') || true

  if [ -n "${alice_token}" ] && [ "${alice_token}" != "null" ]; then
    info "Testing SCAR with alice's OIDC token..."
    local scar_url="https://root.kcp.example:6443/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews"
    curl -sk -X POST -H "Authorization: Bearer ${alice_token}" "${scar_url}" | jq . || warn "SCAR verification failed — check access-vw logs"
  else
    warn "Could not get OIDC token for alice — skipping SCAR verification"
  fi
}

# ─── Main ────────────────────────────────────────────────────────────────────

main() {
  info "Setting up kcp-access-vw Kind environment"
  info ""

  step_kind_cluster
  step_metallb
  step_cert_manager
  step_keycloak_certs
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
  # Resolve the MetalLB external IP assigned to the MCP gateway
  local mcp_ip
  mcp_ip=$(kubectl --kubeconfig "${KIND_KUBECONFIG}" --context "kind-${CLUSTER_NAME}" get svc -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=kcp-mcp-gateway \
    -o jsonpath='{.items[0].status.loadBalancer.ingress[0].ip}' 2>/dev/null || true)
  if [ -z "${mcp_ip}" ]; then
    mcp_ip="<pending — check: kubectl get svc -n envoy-gateway-system>"
  fi

  info "MCP endpoint:  http://mcp.kcp.example:8443/mcp  (IP: ${mcp_ip})"
  info "Keycloak:      https://keycloak.kcp.example:8443  (admin/admin)"
  info "kcp FrontProxy: https://root.kcp.example:6443"
  info "kcp admin:     export KUBECONFIG=${SCRIPT_DIR}/admin.kubeconfig"
  info "Debug graph:   curl -s http://localhost:9099/debug/graph | jq  (after: kubectl port-forward -n ${ACCESS_VW_NS} svc/access-vw 9099:9099)"
  info ""
  info "Add to /etc/hosts:"
  info "  ${mcp_ip}  mcp.kcp.example"
  info "  127.0.0.1  keycloak.kcp.example root.kcp.example"
  info ""
  info "OIDC users:    alice / alice, bob / bob  (realm: kcp)"
  info "Get token:     ${SCRIPT_DIR}/scripts/get-oidc-token.sh alice"
  info ""
  info "To connect Claude Code, add to .mcp.json:"
  info '  { "mcpServers": { "kcp": { "type": "http", "url": "http://mcp.kcp.example:8443/mcp" } } }'
}

main "$@"
