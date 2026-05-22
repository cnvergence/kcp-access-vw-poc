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
           │  Authorization: Bearer <token> passthrough
           ▼
┌──────────────────────────────┐
│  kubernetes-mcp-server       │   ← --cluster-provider=kcp
│  (kcp provider)              │     forwards caller's OIDC token to kcp
└──────────┬───────────────────┘
           │
    ┌──────┴──────┐
    ▼             ▼
┌────────┐  ┌──────────────┐
│  kcp   │  │  access-vw   │   ← SCAR endpoint, behind FrontProxy
│  shard │  │  (SCAR API)  │
└────────┘  └──────────────┘
    ▲
    │  OIDC token validation
    ▼
┌──────────────────────────────┐
│  Keycloak (HTTPS)            │   ← realm: kcp, users: alice, bob
└──────────────────────────────┘
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

The `kubectl-ws` plugin is **not** required — it's installed inside the kcp container. The setup script uses `kubectl ws` via the admin kubeconfig (exposed via Kind NodePort).

## Quick start

```sh
make kind-setup
```

This runs `hack/kind/setup.sh` which deploys everything in order. Takes ~5 minutes on a warm Docker cache.

## What gets deployed

The setup script runs 10 steps sequentially:

| Step | Component | Namespace | What it does |
|------|-----------|-----------|--------------|
| 1 | Kind cluster | — | Creates cluster `kcp-access-vw` with NodePort mappings (8443, 6443) |
| 1b | MetalLB | `metallb-system` | LoadBalancer support for Kind (IP pool `172.18.0.200-250`) |
| 2 | cert-manager | `cert-manager` | TLS certificate management (required by kcp-operator and Keycloak) |
| 4a | Keycloak TLS | `keycloak` | Self-signed CA + TLS certificate for Keycloak HTTPS |
| 3 | Envoy Gateway + AI Gateway | `envoy-gateway-system` | Base gateway + MCP routing controller + `MCPRoute` CRD |
| 4 | kcp | `kcp-system` | kcp-operator → etcd → RootShard → FrontProxy (with OIDC) |
| 5 | Keycloak | `keycloak` | OIDC provider with `kcp` realm, users alice/bob, dynamic client registration |
| 6 | access-vw | `kcp-system` | SCAR service, registered via FrontProxy `additionalPathMappings` |
| 7 | kubernetes-mcp-server | `mcp` | MCP server with `--cluster-provider=kcp`, OIDC token passthrough |
| 8 | MCPRoute | `mcp` | Routes MCP traffic through Envoy with Keycloak OAuth |
| 9 | Test data | — | APIExport, per-user workspaces (alice/bob), RBAC |

## Network access

Services are exposed to the host via two mechanisms — **no port-forwarding needed**:

| Service | Hostname | Port | Mechanism |
|---------|----------|------|-----------|
| Keycloak (HTTPS) | `keycloak.kcp.example` | 8443 | Kind NodePort (30443 → host 8443) |
| kcp FrontProxy | `root.kcp.example` | 6443 | Kind NodePort (30644 → host 6443) |
| MCP Gateway | `mcp.kcp.example` | 8443 | MetalLB LoadBalancer IP |

Add these to `/etc/hosts` (the setup script shows the exact entries):

```
172.18.0.200  mcp.kcp.example
127.0.0.1  keycloak.kcp.example root.kcp.example
```

## OIDC token flow

The full authentication chain works end-to-end with real OIDC tokens:

1. **User authenticates with Keycloak** — gets an OIDC access token (realm `kcp`, client `kcp`)
2. **MCP client sends token** to the Envoy AI Gateway (`http://mcp.kcp.example:8443/mcp`)
3. **Envoy validates the token** via Keycloak's JWKS endpoint
4. **Token passes through** to kubernetes-mcp-server (via `Authorization` header propagation)
5. **kubernetes-mcp-server creates a per-request kcp client** using the caller's bearer token
6. **kcp validates the OIDC token** natively (`spec.auth.oidc` on RootShard/FrontProxy)
7. **kcp identifies the user** (e.g., `alice` from `preferred_username` claim, no prefix)
8. **access-vw/SCAR** returns only the workspaces that user has access to

## Step-by-step walkthrough

If you prefer running steps individually (or need to debug a failed step), here's what each does.

### 1. Create the Kind cluster

```sh
kind create cluster --name kcp-access-vw --config hack/kind/kind-config.yaml
```

The Kind config maps two NodePorts to localhost:
- **30443 → host:8443** — Keycloak HTTPS (`keycloak.kcp.example`)
- **30644 → host:6443** — kcp FrontProxy (`root.kcp.example`)

### 1b. Install MetalLB

```sh
kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/v0.15.3/config/manifests/metallb-native.yaml
```

MetalLB provides LoadBalancer service support in Kind. The IP address pool (`172.18.0.200-250`) is on the Docker `kind` bridge network, making LoadBalancer services directly reachable from the host.

### 2. Install cert-manager

```sh
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.20.2/cert-manager.yaml
kubectl wait --for=condition=Available deployment/cert-manager-webhook -n cert-manager --timeout=120s
```

Required by kcp-operator for shard/proxy TLS certificates and by Keycloak for HTTPS.

### 4a. Create Keycloak TLS certificate

```sh
kubectl apply -f hack/kind/manifests/keycloak/certificate.yaml
```

Creates a self-signed CA (`keycloak-ca`) and a leaf TLS certificate (`keycloak-tls`) for Keycloak's HTTPS endpoint. The CA is copied to `kcp-system` so RootShard and FrontProxy can trust the OIDC issuer.

This step runs before kcp so the CA secret exists when kcp starts with OIDC enabled.

### 3. Install Envoy Gateway + AI Gateway

```sh
helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm \
  --version v1.8.0 \
  --namespace envoy-gateway-system --create-namespace \
  -f hack/kind/manifests/envoy-gateway-values.yaml

helm upgrade --install ai-gateway-crds oci://docker.io/envoyproxy/ai-gateway-crds-helm \
  --version 0.6.0 --namespace envoy-ai-gateway-system --create-namespace

helm upgrade --install ai-gateway oci://docker.io/envoyproxy/ai-gateway-helm \
  --version 0.6.0 --namespace envoy-ai-gateway-system --create-namespace
```

The [Envoy AI Gateway](https://github.com/envoyproxy/ai-gateway) adds native MCP protocol support via the `MCPRoute` CRD. It handles OAuth/OIDC validation, tool multiplexing, and MCP session management.

### 4. Deploy kcp

```sh
kubectl apply -k hack/kind/manifests/kcp-operator     # kcp-operator (image: :main)
kubectl apply -f hack/kind/manifests/kcp/issuer.yaml   # Self-signed cert issuer
kubectl apply -f hack/kind/manifests/kcp/etcd.yaml     # etcd backing store
kubectl apply -f hack/kind/manifests/kcp/root-shard.yaml   # RootShard with OIDC
kubectl apply -f hack/kind/manifests/kcp/front-proxy.yaml  # FrontProxy with OIDC + path mappings
```

Both RootShard and FrontProxy are configured with `spec.auth.oidc`:
- **issuerURL:** `https://keycloak-keycloakx-http.keycloak.svc.cluster.local:8443/realms/kcp`
- **clientID:** `kcp`
- **usernameClaim:** `preferred_username` (no prefix — `alice` not `oidc:alice`)
- **groupsClaim:** `groups` (no prefix)
- **caFileRef:** references the Keycloak CA secret in `kcp-system`

The FrontProxy's `additionalPathMappings` routes `/services/access-virtual-workspace` to the access-vw service.

After the RootShard is ready, the setup script extracts the admin kubeconfig to `hack/kind/admin.kubeconfig`.

### 5. Deploy Keycloak

```sh
helm upgrade --install keycloak codecentric/keycloakx \
  --namespace keycloak \
  -f hack/kind/manifests/keycloak/values.yaml
```

Keycloak runs with HTTPS (cert-manager TLS) using the official `quay.io/keycloak/keycloak` image in dev mode with an embedded H2 database.

Then `hack/kind/scripts/configure-keycloak.sh` configures:

- **Realm:** `kcp`
- **OIDC client `kcp`:** public client for direct user login (password grant)
- **OIDC client `mcp-gateway`:** confidential client for MCP OAuth
- **Client scope:** `mcp-access` with audience mapper
- **Test users:** `alice` / `alice`, `bob` / `bob`
- **Dynamic client registration:** enabled (for MCP clients like Claude Code)

### 6. Build and deploy access-vw

```sh
make kind-build    # or: ./hack/kind/scripts/build-images.sh
kubectl apply -f hack/kind/manifests/access-vw/deployment.yaml
```

Builds the Go binary for `linux/amd64`, packages it in a distroless container, and loads it into Kind. The deployment mounts a kcp admin kubeconfig (generated by kcp-operator `Kubeconfig` CR) and runs with `-trust-headers` (identity comes from FrontProxy's requestheader headers).

### 7. Deploy kubernetes-mcp-server

```sh
helm upgrade --install kubernetes-mcp-server \
  oci://ghcr.io/containers/charts/kubernetes-mcp-server \
  --version 0.1.0 --namespace mcp \
  -f hack/kind/manifests/mcp-server/values.yaml
```

Configured with `--cluster-provider=kcp --toolsets=core,config,kcp --stateless`. Mounts a kcp kubeconfig from a Kubernetes secret (generated by kcp-operator `Kubeconfig` CR).

**Token passthrough:** When an MCP request includes an `Authorization: Bearer` header, the MCP server creates a per-request kcp client using that bearer token instead of its own kubeconfig. This means kcp sees the caller's OIDC identity, not the MCP server's service identity.

### 8. Apply MCPRoute

```sh
kubectl apply -f hack/kind/manifests/mcp-route.yaml
```

This creates:
- **GatewayClass** + **Gateway** — Envoy listener on port 8443
- **EnvoyProxy** — custom Envoy bootstrap config
- **MCPRoute** — routes MCP traffic to `kubernetes-mcp-server:8080` with OAuth:
  - Issuer: Keycloak's `kcp` realm (HTTPS)
  - JWKS: Keycloak's OIDC certs endpoint
  - Protected resource metadata for OAuth discovery

MetalLB assigns the gateway an external IP (e.g., `172.18.0.200`).

### 9. Seed test data

Using the admin kubeconfig:
1. Installs the `access.kcp.io` APIExport in root
2. Creates `alice-workspace` and `bob-workspace`, each with an APIBinding to opt into SCAR indexing
3. Grants each user `cluster-admin` in their own workspace only
4. Verifies SCAR with alice's OIDC token — alice sees `root` + `alice-workspace`, bob sees `root` + `bob-workspace`

## Connecting an MCP client

Ensure your `/etc/hosts` entries are set (see [Network access](#network-access) above), then configure your MCP client:

```json
{
  "mcpServers": {
    "kcp": {
      "type": "http",
      "url": "http://mcp.kcp.example:8443/mcp"
    }
  }
}
```

The MCP client will discover Keycloak OAuth via the protected resource metadata, authenticate, and then interact with kcp workspaces scoped to the authenticated user's RBAC.

## Getting OIDC tokens manually

```sh
# Get a token for alice
hack/kind/scripts/get-oidc-token.sh alice

# Get a token for bob
hack/kind/scripts/get-oidc-token.sh bob

# Use it with curl to test SCAR directly via FrontProxy (exposed on localhost:6443)
TOKEN=$(hack/kind/scripts/get-oidc-token.sh alice)
curl -sk -X POST -H "Authorization: Bearer $TOKEN" \
  https://root.kcp.example:6443/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews | jq
```

## Useful commands

```sh
# kcp admin access
export KUBECONFIG=hack/kind/admin.kubeconfig

# Keycloak admin UI — https://keycloak.kcp.example:8443 (admin/admin, accept self-signed cert)

# access-vw debug graph
kubectl port-forward -n kcp-system svc/access-vw 9099:9099
curl -s http://localhost:9099/debug/graph | jq

# MCP gateway status
kubectl get gateway -A
kubectl get svc -n envoy-gateway-system

# Logs
kubectl logs -n kcp-system -l app=access-vw -f
kubectl logs -n mcp -l app.kubernetes.io/name=kubernetes-mcp-server -f
kubectl logs -n envoy-ai-gateway-system -l app.kubernetes.io/name=ai-gateway -f
kubectl logs -n keycloak keycloak-keycloakx-0 -f
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
- **kubernetes-mcp-server** is the interim MCP server with OIDC token passthrough. It will be replaced by the bespoke MCP Virtual Workspace (Issue #2) which calls the AccessProvider in-process instead of over HTTP.
- **Keycloak** provides OIDC authentication. Both kcp (RootShard + FrontProxy) and the Envoy MCPRoute validate tokens against the same Keycloak realm.
- **MetalLB** provides LoadBalancer IPs on the Kind Docker network, making the MCP gateway directly reachable from the host without port-forwarding.

### Differences from production

| Aspect | This setup | Production |
|--------|-----------|------------|
| TLS | Self-signed CA (cert-manager) | Real certificates |
| kcp | Single shard, embedded cache | Multi-shard, dedicated etcd cluster |
| Gateway | Envoy AI Gateway in-cluster | Edge gateway (Envoy or cloud LB) |
| Keycloak | Local instance, `kcp` realm, H2 DB | External OIDC provider |
| MCP server | `kubernetes-mcp-server` binary | Bespoke MCP VW (Issue #2) |
| access-vw | Standalone service behind FrontProxy | Virtual Workspace in kcp binary |
| Load balancer | MetalLB (L2 mode) | Cloud provider LB |

### Differences from local-testing.md setup

| Aspect | `local-testing.md` | This Kind setup |
|--------|-------------------|-----------------|
| kcp | `kcp start` on host | In-cluster via kcp-operator |
| Auth | curl with `-trust-headers` or `TokenReview` | Real OIDC via Keycloak |
| MCP gateway | None (direct to kubernetes-mcp-server) | Envoy AI Gateway with OAuth |
| SCAR access | Direct HTTP to `localhost:9099` | Via FrontProxy path mapping |
| Token flow | ServiceAccount tokens | Keycloak OIDC tokens (alice/bob) |
| Setup time | Seconds | ~5 minutes |
| Dependencies | Go, kcp binary | Docker, Kind, Helm |
