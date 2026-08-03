# Kind-based testing (full stack)

This deploys the complete ADR 007 architecture into a local Kind cluster:

```
MCP Client (Claude Code, Copilot CLI)
    │
    │  MCP protocol (streamable HTTP)
    │  Authorization: Bearer <OIDC token>
    ▼
┌──────────────────────────────┐
│  kcp FrontProxy              │   ← validates OIDC token,
│  (/services/access + /mcp)   │     forwards identity via
└──────────┬───────────────────┘     requestheader mTLS
           │  X-Remote-User / X-Remote-Group
           ▼
┌──────────────────────────────┐
│  access-vw                   │   ← Built-in MCP server + SCAR API
│  (requestheader · graph)     │     scopes tools to user's workspaces
└──────────┬───────────────────┘
           │
           ▼
┌──────────────────────────────┐
│  kcp shard                   │   ← workspace K8s API calls
│  (per-workspace endpoints)   │     via impersonation
└──────────────────────────────┘
    ▲
    │  OIDC token validation
    ▼
┌──────────────────────────────┐
│  Keycloak (HTTPS)            │   ← realm: kcp, users: alice, bob
└──────────────────────────────┘
```

The **built-in MCP server** in access-vw serves MCP tools at `/services/mcp`, behind the kcp FrontProxy. The front-proxy validates the caller's OIDC token and forwards identity via requestheader mTLS (`X-Remote-User` / `X-Remote-Group` headers) to access-vw. access-vw queries the permission graph for the caller's workspaces and scopes all tool operations to those workspaces only, impersonating the caller for per-workspace kcp calls. No separate MCP server binary is needed.

> **Optional add-on:** An Envoy AI Gateway (MCPRoute + OAuth via Keycloak) can be deployed in front of the front-proxy with `make -C hack/kind ai-gateway`. Local OIDC validation on access-vw itself (for callers bypassing the front-proxy) is likewise opt-in via `make -C hack/kind enable-oidc`.

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
| [mkcert](https://github.com/FiloSottile/mkcert) | Any | Host-trusted TLS certificates for Keycloak |
| Go | 1.26+ | Building access-vw |

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
| 3a | Keycloak TLS | `keycloak` | Host-trusted CA + TLS certificate for Keycloak HTTPS (via mkcert) |
| 3 | Keycloak | `keycloak` | OIDC provider with `kcp` realm, users alice/bob, dynamic client registration |
| 4 | kcp | `kcp-system` | kcp-operator → etcd → RootShard → FrontProxy (with OIDC) |
| 5 | access-vw | `kcp-system` | SCAR API + built-in MCP server, registered via FrontProxy `additionalPathMappings` |
| 6 | Test data | — | APIExport, per-user workspaces (alice/bob), differentiated RBAC |

Optional add-ons (not part of `setup`):

| Target | Component | What it does |
|--------|-----------|--------------|
| `make -C hack/kind ai-gateway` | Envoy Gateway + AI Gateway + MCPRoute | MCP entry point at `http://mcp.kcp.example:8080/mcp` with OAuth via Keycloak, routed to the front-proxy |
| `make -C hack/kind enable-oidc` | access-vw OIDC overlay | Local bearer-token validation for callers that bypass the front-proxy (config must match kcp exactly) |

## Network access

Services are exposed to the host via two mechanisms — **no port-forwarding needed**:

| Service | Hostname | Port | Mechanism |
|---------|----------|------|-----------|
| Keycloak (HTTPS) | `keycloak.kcp.example` | 8443 | Kind NodePort (30443 → host 8443) |
| kcp FrontProxy | `root.kcp.example` | 6443 | Kind NodePort (30644 → host 6443) |
| MCP Gateway (HTTP, optional) | `mcp.kcp.example` | 8080 | MetalLB LoadBalancer IP (only with `make ai-gateway`) |

Add these to `/etc/hosts` (the setup script shows the exact entries):

```
127.0.0.1  keycloak.kcp.example root.kcp.example
172.18.0.200  mcp.kcp.example   # only needed for the optional AI gateway
```

## OIDC token flow

The full authentication chain works end-to-end with real OIDC tokens:

1. **User authenticates with Keycloak** — gets an OIDC access token (realm `kcp`, client `kcp`)
2. **MCP client sends the token** to the kcp front-proxy (`https://root.kcp.example:6443/services/mcp`)
3. **Front-proxy validates the OIDC token** — extracts the user identity (with kcp's prefixes applied) and forwards it as `X-Remote-User` / `X-Remote-Group` headers over requestheader mTLS to access-vw
4. **access-vw queries the permission graph** — returns only workspaces the caller has access to
5. **MCP tools execute against scoped workspaces** — each K8s API call impersonates the caller via access-vw's own kcp identity

With the optional AI gateway (`make -C hack/kind ai-gateway`), steps 2–3 gain a hop: the MCP client talks OAuth to the gateway (`http://mcp.kcp.example:8080/mcp`), Envoy validates the token against Keycloak's JWKS, and forwards the `Authorization` header to the front-proxy, which validates it again and proceeds as above.

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

### 3. Create Keycloak TLS certificate

```sh
hack/kind/scripts/create-keycloak-certs.sh
```

Uses [mkcert](https://github.com/FiloSottile/mkcert) to generate a TLS certificate for `keycloak.kcp.example` that is automatically trusted by the host OS (macOS system keychain / Linux trust store). This is required so MCP clients (which run on the host in Node.js) can complete the OAuth flow over HTTPS without TLS errors. The script creates `keycloak-tls` (for the Keycloak pod) and `keycloak-ca` (for kcp and access-vw OIDC verification) secrets in both the `keycloak` and `kcp-system` namespaces.

This step runs before kcp so the CA secret exists when kcp starts with OIDC enabled.

### 4. Deploy Keycloak

```sh
helm upgrade --install keycloak codecentric/keycloakx \
  --namespace keycloak \
  -f hack/kind/manifests/keycloak/values.yaml
```

Keycloak runs with HTTPS (mkcert TLS) using the official `quay.io/keycloak/keycloak` image in dev mode with an embedded H2 database.

Then `scripts/configure-keycloak.sh` configures:

- **Realm:** `kcp`
- **OIDC client `kcp`:** public client for direct user login (password grant)
- **OIDC client `mcp-gateway`:** confidential client for MCP OAuth
- **Client scope:** `mcp-access` with audience mapper
- **Test users:** `alice` / `alice`, `bob` / `bob`
- **Dynamic client registration:** enabled (for MCP clients like Claude Code)

### 5. Deploy kcp

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

The FrontProxy's `additionalPathMappings` routes `/services/access` and `/services/mcp` to the access-vw service over HTTPS (verified against the kcp root CA), authenticating itself with its requestheader client certificate.

After the RootShard is ready, the Makefile extracts the admin kubeconfig to `hack/kind/admin.kubeconfig`.

### 6. Build and deploy access-vw

```sh
make -C hack/kind install-access-vw    # builds image + deploys
```

Builds the Go binary for `linux/amd64`, packages it in a distroless container, and loads it into Kind. The deployment:

- serves TLS on `:9443` with a cert-manager certificate issued from the kcp-operator's `root-server-ca` issuer (so the FrontProxy can verify it against its existing root-CA mount),
- mounts the `root-requestheader-client-ca` secret and runs with `--requestheader-client-ca-file` / `--requestheader-allowed-names=kcp-front-proxy` / `--requestheader-username-headers=X-Remote-User` / `--requestheader-group-headers=X-Remote-Group` / `--requestheader-extra-headers-prefix=X-Remote-Extra-`, so identity headers are only trusted from the FrontProxy's client certificate,
- mounts a kcp admin kubeconfig (generated by kcp-operator `Kubeconfig` CR) and runs with `--apiexport-endpointslice=access.kcp.io` for multi-shard mode.

There are **no OIDC flags in the base deployment** — the front-proxy has already validated the caller's token by the time a request arrives. To accept bearer tokens from direct callers too, apply the OIDC overlay (`make -C hack/kind enable-oidc`, manifests in `hack/kind/manifests/access-vw-oidc/`); its configuration must match kcp's OIDC flags exactly, including username/groups prefixes, or graph lookups will silently return no clusters.

access-vw serves two virtual workspaces:
- **SCAR API** at `/services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews` — "which workspaces does this user have access to?"
- **Built-in MCP server** at `/services/mcp` — MCP tools scoped to the caller's workspaces (uses the same in-process permission graph as SCAR, no HTTP round-trip)

### 7. Seed test data

Using the admin kubeconfig:
1. Installs the `access.kcp.io` APIExport in root
2. Creates `alice-workspace` and `bob-workspace`, each with an APIBinding to opt into SCAR indexing
3. Grants alice `cluster-admin` in `alice-workspace`; grants bob `view` + `workspace-user` in `bob-workspace` (differentiated RBAC)
4. Verifies SCAR with alice's OIDC token — alice sees only `alice-workspace`, bob sees only `bob-workspace` (root is excluded because it has no APIBinding)

## Connecting an MCP client

Ensure your `/etc/hosts` entries are set (see [Network access](#network-access) above). In the default setup, MCP clients connect directly to the front-proxy with a bearer token:

```json
{
  "mcpServers": {
    "kcp": {
      "type": "http",
      "url": "https://root.kcp.example:6443/services/mcp",
      "headers": {
        "Authorization": "Bearer <token from hack/kind/scripts/get-oidc-token.sh alice>"
      }
    }
  }
}
```

The front-proxy validates the token and forwards the identity to access-vw via requestheader mTLS. All tool operations are scoped to the authenticated user's workspaces.

## Optional: Envoy AI Gateway

For clients that should authenticate interactively via OAuth (dynamic client registration, browser login) instead of a pre-fetched bearer token, deploy the Envoy AI Gateway in front of the front-proxy:

```sh
make -C hack/kind ai-gateway
```

This runs two targets:

**`install-envoy`** — installs [Envoy Gateway](https://gateway.envoyproxy.io/) and the [Envoy AI Gateway](https://github.com/envoyproxy/ai-gateway) (native MCP protocol support via the `MCPRoute` CRD — OAuth/OIDC validation, tool multiplexing, MCP session management) via Helm.

**`install-mcp`** — applies `hack/kind/manifests/ai-gateway`, which creates:
- **Certificate** — self-signed TLS certificate for `mcp.kcp.example` (provisioned for future HTTPS; Gateway currently uses HTTP)
- **GatewayClass** + **Gateway** — Envoy HTTP listener on port 8080
- **EnvoyProxy** — custom Envoy bootstrap config with MCP-specific access logging
- **MCPRoute** — routes MCP traffic to the kcp front-proxy (`frontproxy-front-proxy:6443`) at path `/services/mcp` with OAuth:
  - Issuer: Keycloak's `kcp` realm (HTTPS)
  - JWKS: Keycloak's OIDC certs endpoint
  - `Authorization` header forwarding so the front-proxy receives (and validates) the caller's OIDC token, then forwards the identity to access-vw via requestheader mTLS
  - Protected resource metadata for OAuth discovery (`http://mcp.kcp.example:8080/mcp`)
- **BackendTLSPolicy** — TLS origination for the Envoy → front-proxy hop, validated against the kcp root CA

MetalLB assigns the gateway an external IP (e.g., `172.18.0.200`); add it to `/etc/hosts` as `mcp.kcp.example`. Then point the MCP client at the gateway instead:

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

The MCP client discovers Keycloak OAuth via the protected resource metadata, authenticates, and interacts with kcp workspaces scoped to the authenticated user's permissions.

> **Note:** The AI Gateway prefixes tool names with the backend Service name (e.g. `frontproxy-front-proxy__list_kcp_workspaces`). This is inherent to how the Envoy AI Gateway disambiguates backends.

## Optional: local OIDC on access-vw

By default access-vw trusts only requestheader identities from the front-proxy. To let callers reach access-vw directly with a bearer token (bypassing the front-proxy):

```sh
make -C hack/kind enable-oidc     # apply the OIDC overlay
make -C hack/kind disable-oidc    # revert to requestheader-only
```

The overlay (`hack/kind/manifests/access-vw-oidc/`) adds the `--oidc-*` flags and the Keycloak CA mount. The OIDC configuration **must be identical to kcp's** — issuer, client ID, claims, and username/groups prefixes — because the permission graph indexes RBAC subjects verbatim as kcp stores them; any mismatch makes lookups silently return no clusters.

## Getting OIDC tokens manually

```sh
# Get a token for alice
hack/kind/scripts/get-oidc-token.sh alice

# Get a token for bob
hack/kind/scripts/get-oidc-token.sh bob

# Use it with curl to test SCAR directly via FrontProxy (exposed on localhost:6443)
TOKEN=$(hack/kind/scripts/get-oidc-token.sh alice)
curl -sk -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{}' \
  https://root.kcp.example:6443/services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews | jq
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

# access-vw debug graph (authenticated)
kubectl port-forward -n kcp-system svc/access-vw 9443:9443
TOKEN=$(hack/kind/scripts/get-oidc-token.sh alice)
curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:9443/debug/graph | jq

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
- **kcp FrontProxy** authenticates every SCAR and MCP request (OIDC bearer tokens) and propagates identity as `X-Remote-User` / `X-Remote-Group` headers over requestheader mTLS. The `additionalPathMappings` on the FrontProxy CR register access-vw at `/services/access` and `/services/mcp`.
- **access-vw** is a virtual-workspace root apiserver serving both the SCAR API and the built-in MCP server over TLS. It trusts identity headers from the FrontProxy's requestheader client certificate; per-workspace MCP tool calls impersonate the caller through kcp using access-vw's own kcp identity. access-vw also has its own OIDC authenticator (same config as kcp) as a fallback for direct callers.
- **Keycloak** provides OIDC authentication. Both kcp (RootShard + FrontProxy) and the Envoy MCPRoute validate tokens against the same Keycloak realm.
- **MetalLB** provides LoadBalancer IPs on the Kind Docker network, making the MCP gateway directly reachable from the host without port-forwarding.

### Differences from production

| Aspect | This setup | Production |
|--------|-----------|------------|
| TLS | mkcert CA (host-trusted) + cert-manager (in-cluster) | Real certificates |
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
| Auth | Bearer tokens via `TokenReview` | Real OIDC via Keycloak + FrontProxy requestheader (both SCAR and MCP) |
| MCP | Direct to access-vw `:9443` (no gateway) | Envoy AI Gateway with OAuth → FrontProxy → access-vw |
| SCAR access | Direct HTTPS to `localhost:9443` | Via FrontProxy path mapping |
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
