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
           │  Authorization: Bearer <OIDC token> passthrough
           ▼
┌──────────────────────────────┐
│  access-vw                   │   ← Built-in MCP server + SCAR API
│  (TokenReview · graph scope) │     authenticates via OIDC token,
└──────────┬───────────────────┘     scopes tools to user's workspaces
           │
           ▼
┌──────────────────────────────┐
│  kcp shard                   │   ← workspace K8s API calls
│  (per-workspace endpoints)   │     using caller's bearer token
└──────────────────────────────┘
    ▲
    │  OIDC token validation
    ▼
┌──────────────────────────────┐
│  Keycloak (HTTPS)            │   ← realm: kcp, users: alice, bob
└──────────────────────────────┘
```

The **built-in MCP server** in access-vw serves MCP tools at `/services/access-virtual-workspace/mcp`. It authenticates each request via OIDC token (TokenReview against kcp), queries the permission graph for the caller's workspaces, and scopes all tool operations to those workspaces only. No separate MCP server binary is needed.

> **Alternative:** An upstream `kubernetes-mcp-server` deployment is available via `make install-mcp-upstream` — see [Alternative: upstream kubernetes-mcp-server](#alternative-upstream-kubernetes-mcp-server) below.

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

This delegates to `make -C hack/kind setup` which deploys everything in order. Takes ~5 minutes on a warm Docker cache.

## What gets deployed

The Makefile runs these steps sequentially (`make -C hack/kind setup`):

| Step | Component | Namespace | What it does |
|------|-----------|-----------|--------------|
| 1 | Kind cluster | — | Creates cluster `kcp-access-vw` with NodePort mappings (8443, 6443) |
| 1b | MetalLB | `metallb-system` | LoadBalancer support for Kind (IP pool `172.18.0.200-250`) |
| 2 | cert-manager | `cert-manager` | TLS certificate management (required by kcp-operator and Keycloak) |
| 4a | Keycloak TLS | `keycloak` | Self-signed CA + TLS certificate for Keycloak HTTPS |
| 3 | Envoy Gateway + AI Gateway | `envoy-gateway-system` | Base gateway + MCP routing controller + `MCPRoute` CRD |
| 4 | kcp | `kcp-system` | kcp-operator → etcd → RootShard → FrontProxy (with OIDC) |
| 5 | Keycloak | `keycloak` | OIDC provider with `kcp` realm, users alice/bob, dynamic client registration |
| 6 | access-vw | `kcp-system` | SCAR API + built-in MCP server, registered via FrontProxy `additionalPathMappings` |
| 7 | MCPRoute + AI Gateway | `kcp-system` | Routes MCP traffic through Envoy with OAuth via Keycloak |
| 8 | Test data | — | APIExport, per-user workspaces (alice/bob), differentiated RBAC |

## Network access

Services are exposed to the host via two mechanisms — **no port-forwarding needed**:

| Service | Hostname | Port | Mechanism |
|---------|----------|------|-----------|
| Keycloak (HTTPS) | `keycloak.kcp.example` | 8443 | Kind NodePort (30443 → host 8443) |
| kcp FrontProxy | `root.kcp.example` | 6443 | Kind NodePort (30644 → host 6443) |
| MCP Gateway (HTTP) | `mcp.kcp.example` | 8080 | MetalLB LoadBalancer IP |

Add these to `/etc/hosts` (the setup script shows the exact entries):

```
172.18.0.200  mcp.kcp.example
127.0.0.1  keycloak.kcp.example root.kcp.example
```

## OIDC token flow

The full authentication chain works end-to-end with real OIDC tokens:

1. **User authenticates with Keycloak** — gets an OIDC access token (realm `kcp`, client `kcp`)
2. **MCP client sends token** to the Envoy AI Gateway (`http://mcp.kcp.example:8080/mcp`)
3. **Envoy validates the token** via Keycloak's JWKS endpoint
4. **Token passes through** to access-vw's built-in MCP handler (via `Authorization` header forwarding)
5. **access-vw performs a TokenReview** against kcp — validates the OIDC token and extracts the user identity
6. **access-vw queries the permission graph** — returns only workspaces the caller has access to
7. **MCP tools execute against scoped workspaces** — each K8s API call uses the caller's bearer token against the per-workspace endpoint

## Step-by-step walkthrough

Each step is a separate Makefile target (e.g., `make -C hack/kind install-kcp`). You can run them individually to debug failures.

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

After the RootShard is ready, the Makefile extracts the admin kubeconfig to `hack/kind/admin.kubeconfig`.

### 5. Deploy Keycloak

```sh
helm upgrade --install keycloak codecentric/keycloakx \
  --namespace keycloak \
  -f hack/kind/manifests/keycloak/values.yaml
```

Keycloak runs with HTTPS (cert-manager TLS) using the official `quay.io/keycloak/keycloak` image in dev mode with an embedded H2 database.

Then `scripts/configure-keycloak.sh` configures:

- **Realm:** `kcp`
- **OIDC client `kcp`:** public client for direct user login (password grant)
- **OIDC client `mcp-gateway`:** confidential client for MCP OAuth
- **Client scope:** `mcp-access` with audience mapper
- **Test users:** `alice` / `alice`, `bob` / `bob`
- **Dynamic client registration:** enabled (for MCP clients like Claude Code)

### 6. Build and deploy access-vw

```sh
make -C hack/kind install-access-vw    # builds image + deploys
```

Builds the Go binary for `linux/amd64`, packages it in a distroless container, and loads it into Kind. The deployment mounts a kcp admin kubeconfig (generated by kcp-operator `Kubeconfig` CR) and runs with `-trust-headers` and `-apiexport-endpointslice=access.kcp.io` for multi-shard mode (identity comes from FrontProxy's requestheader headers).

access-vw serves two endpoints:
- **SCAR API** at `/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews` — "which workspaces does this user have access to?"
- **Built-in MCP server** at `/services/access-virtual-workspace/mcp` — MCP tools scoped to the caller's workspaces (uses the same in-process permission graph as SCAR, no HTTP round-trip)

### 7. Apply MCPRoute + AI Gateway

```sh
kubectl apply -k hack/kind/manifests/ai-gateway
```

This creates:
- **Certificate** — self-signed TLS certificate for `mcp.kcp.example` (provisioned for future HTTPS; Gateway currently uses HTTP)
- **GatewayClass** + **Gateway** — Envoy HTTP listener on port 8080
- **EnvoyProxy** — custom Envoy bootstrap config with MCP-specific access logging
- **MCPRoute** — routes MCP traffic to `access-vw:9099` at path `/services/access-virtual-workspace/mcp` with OAuth:
  - Issuer: Keycloak's `kcp` realm (HTTPS)
  - JWKS: Keycloak's OIDC certs endpoint
  - `Authorization` header forwarding so access-vw receives the caller's OIDC token
  - Protected resource metadata for OAuth discovery (`http://mcp.kcp.example:8080/mcp`)

MetalLB assigns the gateway an external IP (e.g., `172.18.0.200`).

### 8. Seed test data

Using the admin kubeconfig:
1. Installs the `access.kcp.io` APIExport in root
2. Creates `alice-workspace` and `bob-workspace`, each with an APIBinding to opt into SCAR indexing
3. Grants alice `cluster-admin` in `alice-workspace`; grants bob `view` + `workspace-user` in `bob-workspace` (differentiated RBAC)
4. Verifies SCAR with alice's OIDC token — alice sees only `alice-workspace`, bob sees only `bob-workspace` (root is excluded because it has no APIBinding)

## Connecting an MCP client

Ensure your `/etc/hosts` entries are set (see [Network access](#network-access) above), then configure your MCP client:

```json
{
  "mcpServers": {
    "kcp": {
      "type": "http",
      "url": "http://mcp.kcp.example:8080/mcp"
    }
  }
}
```

The Envoy AI Gateway handles OAuth (via Keycloak) and forwards the caller's OIDC token to access-vw. The MCP client will discover Keycloak OAuth via the protected resource metadata, authenticate, and then interact with kcp workspaces scoped to the authenticated user's permissions.

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

# Get OIDC token for a user
make -C hack/kind get-token USER=alice

# Test SCAR for a user
make -C hack/kind scar USER=alice
make -C hack/kind scar USER=bob

# Keycloak admin UI — https://keycloak.kcp.example:8443 (admin/admin, accept self-signed cert)

# access-vw debug graph
kubectl port-forward -n kcp-system svc/access-vw 9099:9099
curl -s http://localhost:9099/debug/graph | jq

# MCP gateway status
kubectl get gateway -A
kubectl get certificate -n kcp-system

# Test MCP endpoint directly
TOKEN=$(hack/kind/scripts/get-oidc-token.sh alice)
curl -s http://mcp.kcp.example:8080/mcp -H "Authorization: Bearer $TOKEN"

# Logs
kubectl logs -n kcp-system -l app=access-vw -f
kubectl logs -n envoy-ai-gateway-system -l app.kubernetes.io/name=ai-gateway -f
kubectl logs -n keycloak keycloak-keycloakx-0 -f

# List all available Makefile targets
make -C hack/kind help
```

## Rebuilding access-vw

After code changes, rebuild and redeploy without recreating the cluster:

```sh
make -C hack/kind install-access-vw
```

This rebuilds the image, loads it into Kind, and restarts the deployment.

## Teardown

```sh
make kind-teardown
```

This deletes the Kind cluster and cleans up `hack/kind/admin.kubeconfig`.

## Architecture notes

### How the pieces fit together

The setup mirrors the ADR 007 production architecture:

- **Envoy AI Gateway** fills the "MCP-aware gateway" role from the ADR. It handles OIDC (via Keycloak), MCP protocol routing, and session management. In production this would be the edge gateway; here it runs in-cluster.
- **kcp FrontProxy** handles identity propagation (`X-Remote-User`, `X-Remote-Group`) for SCAR requests reaching the access-vw. The `additionalPathMappings` on the FrontProxy CR registers access-vw at `/services/access-virtual-workspace`.
- **access-vw** serves both the SCAR API and a built-in MCP server. The MCP handler authenticates callers via TokenReview (validating the forwarded OIDC token against kcp), queries the in-process permission graph, and scopes all tool operations to the caller's workspaces. SCAR requests arriving via FrontProxy use `-trust-headers` for identity.
- **Keycloak** provides OIDC authentication. Both kcp (RootShard + FrontProxy) and the Envoy MCPRoute validate tokens against the same Keycloak realm.
- **MetalLB** provides LoadBalancer IPs on the Kind Docker network, making the MCP gateway directly reachable from the host without port-forwarding.

### Differences from production

| Aspect | This setup | Production |
|--------|-----------|------------|
| TLS | Self-signed CA (cert-manager) | Real certificates |
| kcp | Single shard, multi-shard code path, embedded cache | Multi-shard, dedicated etcd per shard |
| Gateway | Envoy AI Gateway (HTTP, self-signed cert provisioned) | Edge gateway (Envoy or cloud LB, real certs, HTTPS) |
| Keycloak | Local instance, `kcp` realm, H2 DB | External OIDC provider |
| MCP server | Built-in MCP in access-vw | Same (or bespoke MCP VW in kcp binary) |
| access-vw | Standalone service behind FrontProxy | Virtual Workspace in kcp binary |
| Load balancer | MetalLB (L2 mode) | Cloud provider LB |

### Differences from local-testing.md setup

| Aspect | `local-testing.md` | This Kind setup |
|--------|-------------------|-----------------|
| kcp | `kcp start` on host | In-cluster via kcp-operator |
| Auth | curl with `-trust-headers` or `TokenReview` | Real OIDC via Keycloak |
| MCP | Direct to access-vw `:9099` (no gateway) | Envoy AI Gateway with OAuth → access-vw |
| SCAR access | Direct HTTP to `localhost:9099` | Via FrontProxy path mapping |
| Token flow | ServiceAccount tokens | Keycloak OIDC tokens (alice/bob) |
| Setup time | Seconds | ~5 minutes |
| Dependencies | Go, kcp binary | Docker, Kind, Helm |

## Alternative: upstream kubernetes-mcp-server

Instead of the built-in MCP server, you can deploy the upstream [kubernetes-mcp-server](https://github.com/containers/kubernetes-mcp-server) as a separate pod. This is useful for comparing behavior or if you need kcp-specific toolsets (`--cluster-provider=kcp`) that the built-in server doesn't cover.

### Deploy

```sh
make -C hack/kind install-mcp-upstream
```

This deploys a `Kubeconfig` CR (generates a kcp admin kubeconfig secret) and installs kubernetes-mcp-server via Helm with `--cluster-provider=kcp --toolsets=core,config,kcp --stateless`.

### Switch the MCPRoute backend

After deploying, update `hack/kind/manifests/ai-gateway/mcp-route.yaml` to point at the upstream server instead of access-vw:

```yaml
  backendRefs:
    - name: mcp-server-kubernetes-mcp-server
      kind: Service
      port: 8080
      path: /mcp
      forwardHeaders:
        - name: Authorization
```

Then re-apply:

```sh
kubectl apply -k hack/kind/manifests/ai-gateway
```

### Remove

```sh
make -C hack/kind uninstall-mcp-upstream
```

> **Note:** The upstream MCP server uses an admin-level kubeconfig. When an MCP request includes a bearer token, it creates a per-request kcp client using that token (token passthrough). Without SCAR integration, it lists all workspaces the kubeconfig can reach; the AI Gateway's OAuth ensures only authenticated users can access the MCP endpoint.
