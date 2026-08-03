# kcp-access-vw

Permission-aware workspace discovery for [kcp](https://www.kcp.io/). Implements the **Access Virtual Workspace** — a lightweight HTTP service that answers "which workspaces does this user have access to?" with a single API call (a **SelfClusterAccessReview**, or **SCAR**) instead of N individual `SelfSubjectAccessReviews`. Includes a **built-in MCP server** that exposes kcp workspace tools scoped to the caller's permissions.

## How it works

```
┌──────────────┐         ┌──────────────────┐
│  MCP client  │◀────────│  access-vw       │
│  (Copilot,   │  scoped │  ┌────────────┐  │
│   Claude)    │  tools  │  │ MCP server │  │
└──────────────┘         │  └─────┬──────┘  │
                         │        │         │
                         │  ┌─────┴──────┐  │
                         │  │ permission │  │
                         │  │   graph    │  │
                         │  └─────┬──────┘  │
                         │        │         │
                         │  ┌─────┴──────┐  │
                         │  │ RBAC watch │  │
                         │  │ (CRBs/RBs) │  │
                         │  └────────────┘  │
                         └──────────────────┘
```

1. **Indexing:** The server watches `ClusterRoleBindings` and `RoleBindings` across every kcp workspace that has bound the `access.kcp.io` APIExport. These bindings are translated into an in-memory permission graph mapping subjects (users, groups, service accounts) to logical clusters.

2. **Querying (SCAR):** A caller POSTs to the SCAR endpoint with a bearer token (or, behind kcp's front-proxy, identity forwarded via requestheader mTLS). The apiserver's authentication layer resolves the caller's identity and the server returns the list of `(clusterName, endpoint)` pairs the caller can access.

3. **MCP tools:** The built-in MCP server takes the caller identity resolved by the same authentication layer, queries the same in-process permission graph, and exposes Kubernetes + kcp tools scoped to only the caller's authorized workspaces. Per-workspace calls impersonate the caller using the server's own kcp identity. Tools include `list_resources`, `get_resource`, `create_resource`, `update_resource`, `delete_resource`, and kcp-specific `list_kcp_workspaces` / `create_kcp_workspace`.

4. **Consuming (SCAR):** The SCAR response can also feed into any client that understands kubeconfig — a CLI, a dashboard, etc. The included `scar-to-kubeconfig` tool converts SCAR output into a scoped kubeconfig directly.

## Components

| Path | Description |
|------|-------------|
| `cmd/server` | Main binary. Runs the RBAC indexer + the virtual-workspace root apiserver. |
| `cmd/init` | Init container binary. Bootstraps the `access.kcp.io` APIExport, schemas, and RBAC into kcp. |
| `cmd/scar-to-kubeconfig` | Helper that calls SCAR and writes a scoped kubeconfig. |
| `pkg/server` | Options + wiring: root apiserver, built-in authentication (OIDC, requestheader, client certs), per-VW authorization. |
| `pkg/graph` | In-memory permission graph. No kcp imports — cleanly extractable. |
| `pkg/rbacprovider` | Watches CRBs/RBs via multicluster-runtime, translates into graph grants. |
| `pkg/virtual/mcp` | MCP virtual workspace (`/services/mcp`). Scopes tools to the authenticated caller's workspaces; per-workspace calls use impersonation. |
| `pkg/virtual/mcp/tools` | MCP tool handlers: generic K8s resources + kcp-specific (workspaces). |
| `pkg/virtual/scar` | Access virtual workspace (`/services/access`): create-only REST storage for SelfClusterAccessReview. Reads from the graph. |
| `pkg/bootstrap` | Init bootstrapping logic for seeding APIExport, schemas, and RBAC into kcp. |
| `pkg/generated/openapi` | Generated OpenAPI definitions for the access API (openapi-gen). |
| `pkg/apis/access/v1alpha1` | `SelfClusterAccessReview` API types. |
| `config/apiexport` | kcp APIExport + APIResourceSchema manifests for `access.kcp.io`. |
| `config/deployment` | Kubernetes Deployment manifest for the controller. |
| `config/examples` | Example APIBinding for consumer workspaces to opt in. |
| `config/rbac` | Per-user RBAC seed files (alice=cluster-admin, bob=view+workspace-user). |
| `hack/kind/` | Kind-based full-stack setup (Makefile, manifests, helm values, scripts). |
| `docs/` | Testing guides ([local](docs/local-testing.md), [Kind](docs/kind-testing.md)). |
| `site/` | GitHub Pages documentation site. |

## Quick start

### Prerequisites

- Go 1.26+
- kcp running locally (`kcp start`)
- `kubectl` with the [`kubectl-ws` plugin](https://github.com/kcp-dev/kcp)
- `jq` (for reading JSON responses)

### Build

```sh
make build    # produces bin/access-vw and bin/scar-to-kubeconfig
```

### Local dev flow

```sh
# 1. Start kcp
kcp start

# 2. Install the APIExport in root
make install-apiexport

# 3. Create a test workspace and seed RBAC
make create-test-workspaces
make seed-rbac

# 4. Start the server (TLS on :9443, bearer tokens via TokenReview)
make run-access-vw


The demo walks through authorized users, unauthorized users, group access, and dynamic grant/revoke — with pass/fail assertions for each scenario.

See [`docs/local-testing.md`](docs/local-testing.md) for the full walkthrough.

### Kind-based setup (full stack)

Deploys the complete ADR 007 architecture into a local Kind cluster — kcp (single shard, multi-shard code path), Keycloak (OIDC with mkcert-trusted TLS), and access-vw with built-in MCP server, all served through kcp's front-proxy. An Envoy AI Gateway in front of the front-proxy is an optional add-on (`make -C hack/kind ai-gateway`). Requires [mkcert](https://github.com/FiloSottile/mkcert) for host-trusted Keycloak certificates:

```sh
make kind-setup     # ~5 min, creates everything
make kind-teardown  # delete the cluster
```

The setup creates per-user workspaces (`alice-workspace`, `bob-workspace`) with differentiated RBAC and verifies SCAR end-to-end with real OIDC tokens. Available Makefile targets:

```sh
make -C hack/kind help          # list all targets
make -C hack/kind scar USER=alice   # test SCAR for a user
make -C hack/kind get-token USER=bob  # get OIDC token
```

See [`docs/kind-testing.md`](docs/kind-testing.md) for the full walkthrough and architecture details.

### MCP demo (lightweight)

Proves end-to-end that an MCP client sees only the workspaces SCAR authorizes, using host-local `kcp start` (no Kind):

```sh
# 1. Start the server (bearer tokens validated via TokenReview)
make run-access-vw

# 2. Connect your MCP client directly to the built-in MCP endpoint
#    Add to your MCP client config:
#    { "mcpServers": { "kcp": { "type": "http", "url": "https://localhost:9443/services/mcp" } } }
#    (dev serving cert is self-signed — the client must skip TLS verification)
```

Alternatively, use `scar-to-kubeconfig` to generate a scoped kubeconfig and feed it to the upstream `kubernetes-mcp-server`:

```sh
make mcp-demo    # generates a scoped kubeconfig from SCAR
kubernetes-mcp-server --kubeconfig=scar.kubeconfig --cluster-provider=kcp
```

See [`docs/local-testing.md`](docs/local-testing.md) for the full walkthrough.

### Cleanup

```sh
make cleanup         # removes RBAC, test workspace, and APIExport (local kcp)
make kind-teardown   # deletes the Kind cluster
```

## SCAR API

**Endpoint:** `POST /services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews`

**Request:** Bearer token in `Authorization` header (validated via TokenReview against kcp), or — behind kcp's front-proxy — identity forwarded as `X-Remote-*` headers over requestheader mTLS.

**Response:**

```json
{
  "kind": "SelfClusterAccessReview",
  "apiVersion": "access.kcp.io/v1alpha1",
  "status": {
    "clusters": [
      {
        "clusterName": "33daicwbox20zsxj",
        "endpoint": "https://kcp.example.com/clusters/33daicwbox20zsxj"
      }
    ]
  }
}
```

## Debug endpoint

```sh
curl -ks -H "Authorization: Bearer $TOKEN" https://localhost:9443/debug/graph | jq
```

Returns the current graph state: all subjects and their cluster mappings. Requires an authenticated caller (any authenticated user).

## MCP endpoint

**Endpoint:** `POST /services/mcp`

The built-in MCP server uses [streamable HTTP](https://modelcontextprotocol.io/specification/2025-03-26/basic/transports#streamable-http) transport (stateless mode). Each request takes the identity resolved by the apiserver authentication layer, queries the permission graph, and returns tools scoped to the caller's workspaces. Per-workspace tool calls impersonate the caller via the server's own kcp identity (which needs RBAC permission to impersonate users, groups, and userextras).

**Available tools:**

| Tool | Description |
|------|-------------|
| `list_kcp_workspaces` | List workspaces accessible to the caller |
| `create_kcp_workspace` | Create a child workspace |
| `list_resources` | List any Kubernetes resource type in a workspace |
| `get_resource` | Get a specific resource by name |
| `create_resource` | Create a resource from YAML/JSON |
| `update_resource` | Update an existing resource |
| `delete_resource` | Delete a resource |

## Architecture

The serving side is a **virtual-workspace root apiserver** ([kcp virtual-workspace-framework](https://github.com/kcp-dev/virtual-workspace-framework)) that serves TLS only (`--secure-port`, default 9443; self-signs a dev cert if none given) and hosts two virtual workspaces:

- **`access`** at `/services/access` — SCAR as a real create-only REST resource (modelled on `SelfSubjectAccessReview`), with kube codecs, content negotiation, and discovery.
- **`mcp`** at `/services/mcp` — the streamable-HTTP MCP handler as a raw-handler VW behind the same filter chain.

The RBAC indexer supports two run modes:

- **Multi-shard** (`--kubeconfig` + `--apiexport-endpointslice`): Production mode. Uses the kcp apiexport provider via multicluster-runtime to watch RBAC bindings across all workspaces bound to the `access.kcp.io` APIExport. Only workspaces with an APIBinding for `access.kcp.io` are indexed — this is the opt-in design.
- **Single-shard** (`--kubeconfig` only): Development mode. Standard client-go informers against one cluster.

Authentication is built-in via `BuiltInAuthenticationOptions` from kube-apiserver (the same pattern as kcp's own front-proxy):
1. **Front-proxy requestheader mTLS (default)** — `X-Remote-User` / `X-Remote-Group` / `X-Remote-Extra-*` headers are trusted only from clients presenting a certificate signed by `--requestheader-client-ca-file` (optionally restricted with `--requestheader-allowed-names`). This is how kcp's front-proxy forwards identity: it validates the caller's bearer token, applies kcp's OIDC prefixes, and forwards the resolved identity — so graph lookups match RBAC bindings by construction.
2. **OIDC (opt-in)** — `--oidc-issuer-url`, `--oidc-client-id`, `--oidc-username-claim`, etc. For callers that reach access-vw directly, bypassing the front-proxy. Tokens are validated locally against the OIDC provider's JWKS — no `TokenReview` round-trip. Configuration **must be identical to kcp's** (including username/groups prefixes) so identities resolve exactly as RBAC bindings store them; see `config/deployment/oidc-patch.yaml`.
3. **Client certificate** — validated against `--client-ca-file`.
4. **Anonymous** — enabled for health probes (`/readyz`, `/livez`) which pass through the always-allow-paths authorizer.

> **Security note:** There is no unauthenticated header-trust mode. Header trust is gated on requestheader client-certificate mTLS — the same pattern the Kubernetes API server aggregation layer uses. Behind the front-proxy the caller's original bearer token never reaches access-vw, so per-workspace MCP calls **impersonate** the caller (`Impersonate-User` / `Impersonate-Group`) using the server's own kubeconfig identity; kcp re-authorizes every impersonated request and audit logs record both identities.

## Deployment

See [`config/README.md`](config/README.md) for production deployment instructions covering:
1. System APIExport installation
2. Controller deployment
3. Consumer workspace opt-in (APIBinding)
4. Front-proxy routing

## Status

> **Proof of concept — SCAR + built-in MCP working end-to-end.** The Kind setup demonstrates the full ADR 007 architecture with a single-shard kcp deployment running the multi-shard code path (`-apiexport-endpointslice`): OIDC authentication via Keycloak at the front-proxy, SCAR and MCP served through the front-proxy with requestheader identity forwarding, per-user workspace scoping via the in-process permission graph, and RBAC indexing via the APIExport provider. An Envoy AI Gateway with OAuth is available as an opt-in add-on. The built-in MCP server exposes kcp workspace tools scoped to the authenticated caller — no separate MCP server binary needed. Expect APIs and package layout to evolve.

📖 **Documentation site:** [cnvergence.github.io/kcp-access-vw-poc](https://cnvergence.github.io/kcp-access-vw-poc/)

## License

See [LICENSE](LICENSE).
