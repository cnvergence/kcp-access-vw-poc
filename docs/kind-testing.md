# Kind-based testing (full stack)

This deploys the complete ADR 007 architecture into a local Kind cluster:

```
MCP Client (Claude Code, Copilot CLI)
    │
    │  MCP protocol (streamable HTTP)
    ▼
┌──────────────────────────────┐
│  Envoy AI Gateway            │   ← MCPRoute CRD, OAuth via Keycloak
│  (MCPRoute + OIDC)           │
└──────────┬───────────────────┘
           │
           ▼
┌──────────────────────────────┐
│  kubernetes-mcp-server       │   ← --cluster-provider=kcp
│  (kcp provider)              │
└──────────┬───────────────────┘
           │
    ┌──────┴──────┐
    ▼             ▼
┌────────┐  ┌──────────────┐
│  kcp   │  │  access-vw   │   ← SCAR endpoint, behind FrontProxy
│  shard │  │  (SCAR API)  │
└────────┘  └──────────────┘
```

For the simpler host-local `kcp start` setup (no Kind, no Keycloak, no gateway), see [`local-testing.md`](local-testing.md).

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| [Docker](https://docs.docker.com/get-docker/) | Any | Kind runs containers in Docker |
| [kind](https://kind.sigs.k8s.io/) | v0.25+ | Local Kubernetes clusters |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | v1.31+ | Kubernetes CLI |
| [Helm](https://helm.sh/docs/intro/install/) | v3.16+ | Chart deployment |
| [jq](https://jqlang.github.io/jq/) | Any | JSON output formatting |
| Go | 1.25+ | Building access-vw |

The `kubectl-ws` plugin is **not** required — it's installed inside the kcp container. The setup script uses `kubectl ws` via port-forward to the kcp admin API.

## Quick start

```sh
make kind-setup
```

This runs `hack/kind/setup.sh` which deploys everything in order. Takes ~5 minutes on a warm Docker cache.

## What gets deployed

The setup script runs 9 steps sequentially:

| Step | Component | Namespace | What it does |
|------|-----------|-----------|--------------|
| 1 | Kind cluster | — | Creates cluster `kcp-access-vw` with port 8443 mapped |
| 2 | cert-manager | `cert-manager` | TLS certificate management (required by kcp-operator) |
| 3 | Envoy Gateway + AI Gateway | `envoy-gateway-system`, `envoy-ai-gateway-system` | Base gateway + MCP routing controller + `MCPRoute` CRD |
| 4 | kcp | `kcp-system` | kcp-operator → etcd → RootShard → FrontProxy |
| 5 | Keycloak | `keycloak` | OIDC provider with "welcome" realm + `mcp-gateway` client |
| 6 | access-vw | `kcp-system` | SCAR service, registered as FrontProxy path mapping |
| 7 | kubernetes-mcp-server | `default` | MCP server with `--cluster-provider=kcp` |
| 8 | MCPRoute | `default` | Routes MCP traffic through Envoy with Keycloak OAuth |
| 9 | Test data | — | APIExport, test workspace, RBAC for `alice`/`eng`/`platform` |

## Step-by-step walkthrough

If you prefer running steps individually (or need to debug a failed step), here's what each does.

### 1. Create the Kind cluster

```sh
kind create cluster --name kcp-access-vw --config hack/kind/kind-config.yaml
```

Port mapping: host `8443` → node `31000` (HTTPS ingress for kcp + MCP).

### 2. Install cert-manager

```sh
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.17.2/cert-manager.yaml
kubectl wait --for=condition=Available deployment/cert-manager-webhook -n cert-manager --timeout=120s
```

Required by kcp-operator for issuing TLS certificates to shards and the front-proxy.

### 3. Install Envoy Gateway + AI Gateway

```sh
# Base Envoy Gateway with AI Gateway extension hooks
helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm \
  --version v1.4.0 \
  --namespace envoy-gateway-system --create-namespace \
  -f hack/kind/manifests/envoy-gateway-values.yaml

# AI Gateway CRDs (MCPRoute, etc.)
helm upgrade --install ai-gateway-crds oci://docker.io/envoyproxy/ai-gateway-crds-helm \
  --version v0.5.1 \
  --namespace envoy-ai-gateway-system --create-namespace

# AI Gateway controller
helm upgrade --install ai-gateway oci://docker.io/envoyproxy/ai-gateway-helm \
  --version v0.5.1 \
  --namespace envoy-ai-gateway-system --create-namespace
```

The [Envoy AI Gateway](https://github.com/envoyproxy/ai-gateway) adds native MCP protocol support to Envoy Gateway via the `MCPRoute` CRD. It handles OAuth/OIDC validation, tool multiplexing, and MCP session management.

### 4. Deploy kcp

```sh
# kcp-operator (manages RootShard, FrontProxy CRDs)
kubectl apply -k https://github.com/kcp-dev/kcp-operator/config/default?ref=main

# Self-signed cert issuer
kubectl apply -f hack/kind/manifests/kcp/issuer.yaml

# etcd (kcp backing store)
kubectl apply -f hack/kind/manifests/kcp/etcd.yaml

# RootShard (kcp API server)
kubectl apply -f hack/kind/manifests/kcp/root-shard.yaml

# FrontProxy (identity propagation + path routing)
kubectl apply -f hack/kind/manifests/kcp/front-proxy.yaml
```

The FrontProxy is configured with an `additionalPathMappings` entry that routes `/services/access-virtual-workspace` to the access-vw service. This is how SCAR requests reach our service through kcp's standard identity propagation.

After the RootShard is ready, the setup script extracts the admin kubeconfig to `hack/kind/admin.kubeconfig`.

### 5. Deploy Keycloak

```sh
helm upgrade --install keycloak oci://registry-1.docker.io/bitnamicharts/keycloak \
  --namespace keycloak --create-namespace \
  -f hack/kind/manifests/keycloak/values.yaml
```

Then `hack/kind/scripts/configure-keycloak.sh` configures:

- **Realm:** `welcome`
- **OIDC client:** `mcp-gateway` (confidential, service accounts enabled)
- **Client scope:** `mcp-access` with audience mapper
- **Dynamic client registration:** enabled for MCP clients like Claude Code

The client secret is stored in the `mcp-gateway-keycloak` Kubernetes secret.

### 6. Build and deploy access-vw

```sh
make kind-build    # or: ./hack/kind/scripts/build-images.sh
kubectl apply -f hack/kind/manifests/access-vw/deployment.yaml
```

This builds the Go binary for `linux/amd64`, packages it in a distroless container, and loads it into Kind. The deployment mounts a kcp admin kubeconfig and runs with `-trust-headers` (identity comes from FrontProxy headers).

### 7. Deploy kubernetes-mcp-server

```sh
helm upgrade --install kubernetes-mcp-server \
  oci://ghcr.io/containers/charts/kubernetes-mcp-server \
  --version 0.1.0 \
  -f hack/kind/manifests/mcp-server/values.yaml
```

Configured with `--cluster-provider=kcp --toolsets=core,config,kcp --stateless`. Mounts a kcp kubeconfig from a Kubernetes secret.

### 8. Apply MCPRoute

```sh
kubectl apply -f hack/kind/manifests/mcp-route.yaml
```

This creates:
- **GatewayClass** + **Gateway** — Envoy listener on port 1975
- **MCPRoute** — routes MCP traffic to `kubernetes-mcp-server:8080` with OAuth:
  - Issuer: Keycloak's `welcome` realm
  - JWKS: Keycloak's OIDC certs endpoint
  - Protected resource metadata for OAuth discovery

### 9. Seed test data

Using the admin kubeconfig:
1. Installs the `access.kcp.io` APIExport in root
2. Creates `test-workspace` with an APIBinding to opt into SCAR indexing
3. Seeds RBAC: ClusterRoleBindings for user `alice`, groups `eng` and `platform`
4. Verifies SCAR returns the expected workspace for `alice`

## Connecting an MCP client

After setup completes:

```sh
# Claude Code / .mcp.json
{
  "mcpServers": {
    "kcp": {
      "type": "http",
      "url": "https://localhost:8443/mcp"
    }
  }
}
```

The MCP client will be redirected to Keycloak for OAuth authentication. After login, the gateway forwards authenticated MCP requests to kubernetes-mcp-server, which serves only the workspaces the user has access to.

## Useful commands

```sh
# kcp admin access
export KUBECONFIG=hack/kind/admin.kubeconfig

# Keycloak admin UI
kubectl port-forward -n keycloak svc/keycloak 8080:80
# → http://localhost:8080 (admin/admin)

# access-vw debug graph
kubectl port-forward -n kcp-system svc/access-vw 9099:9099
curl -s http://localhost:9099/debug/graph | jq

# SCAR via FrontProxy (trusted headers)
curl -sk -X POST -H 'X-Remote-User: alice' \
  https://localhost:8443/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews | jq

# Logs
kubectl logs -n kcp-system -l app=access-vw -f
kubectl logs -n default -l app.kubernetes.io/name=kubernetes-mcp-server -f
kubectl logs -n envoy-ai-gateway-system -l app.kubernetes.io/name=ai-gateway -f
```

## Rebuilding access-vw

After code changes, rebuild and redeploy without recreating the cluster:

```sh
make kind-build
kubectl rollout restart deployment/access-vw -n kcp-system
```

## Teardown

```sh
make kind-teardown
```

This deletes the Kind cluster and cleans up `hack/kind/admin.kubeconfig`.

## Architecture notes

### How the pieces fit together

The setup mirrors the ADR 007 production architecture:

- **Envoy AI Gateway** fills the "MCP-aware gateway" role from the ADR. It handles OIDC (via Keycloak), MCP protocol routing, and session management. In production this would be the edge gateway; here it runs in-cluster.
- **kcp FrontProxy** handles identity propagation (`X-Remote-User`, `X-Remote-Group`) for requests reaching the access-vw. The `additionalPathMappings` on the FrontProxy CR registers access-vw at `/services/access-virtual-workspace`.
- **access-vw** runs with `-trust-headers` behind FrontProxy, the same as it would in production. The FrontProxy is the trust boundary.
- **kubernetes-mcp-server** is the interim MCP server. It will be replaced by the bespoke MCP Virtual Workspace (Issue #2) which calls the AccessProvider in-process instead of over HTTP.

### Differences from production

| Aspect | This setup | Production |
|--------|-----------|------------|
| TLS | Self-signed (cert-manager) | Real certificates |
| kcp | Single shard, embedded cache | Multi-shard, dedicated etcd cluster |
| Gateway | Envoy AI Gateway in-cluster | Edge gateway (Envoy or cloud LB) |
| Keycloak | Local instance, `welcome` realm | External OIDC provider |
| MCP server | `kubernetes-mcp-server` binary | Bespoke MCP VW (Issue #2) |
| access-vw | Standalone service behind FrontProxy | Virtual Workspace in kcp binary |

### Differences from local-testing.md setup

| Aspect | `local-testing.md` | This Kind setup |
|--------|-------------------|-----------------|
| kcp | `kcp start` on host | In-cluster via kcp-operator |
| Auth | curl with `-trust-headers` or `TokenReview` | Real OIDC via Keycloak |
| MCP gateway | None (direct to kubernetes-mcp-server) | Envoy AI Gateway with OAuth |
| SCAR access | Direct HTTP to `localhost:9099` | Via FrontProxy path mapping |
| Setup time | Seconds | ~5 minutes |
| Dependencies | Go, kcp binary | Docker, Kind, Helm |
