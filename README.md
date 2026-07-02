# kcp-access-vw

Permission-aware workspace discovery for [kcp](https://www.kcp.io/). Implements the **Access Virtual Workspace** — a lightweight HTTP service that answers "which workspaces does this user have access to?" with a single API call (a **SelfClusterAccessReview**, or **SCAR**) instead of N individual `SelfSubjectAccessReviews`.

## How it works

```
┌──────────────┐         ┌──────────────────┐         ┌─────────┐
│  MCP client  │◀────────│  MCP server      │◀────────│  SCAR   │
│  (Copilot,   │  scoped │  (kube-mcp)      │  scoped │  HTTP   │
│   Claude)    │  tools  │                  │  config │  API    │
└──────────────┘         └──────────────────┘         └────┬────┘
                                                           │
                                                ┌──────────┴───────────┐
                                                │   In-memory RBAC     │
                                                │   permission graph   │
                                                │                      │
                                                │  watches CRBs/RBs    │
                                                │  across all bound    │
                                                │  workspaces          │
                                                └──────────────────────┘
```

1. **Indexing:** The server watches `ClusterRoleBindings` and `RoleBindings` across every kcp workspace that has bound the `access.kcp.io` APIExport. These bindings are translated into an in-memory permission graph mapping subjects (users, groups, service accounts) to logical clusters.

2. **Querying:** A caller POSTs to the SCAR endpoint with a bearer token (or trusted headers behind a front-proxy). The server resolves the caller's identity and returns the list of `(clusterName, endpoint)` pairs the caller can access.

3. **Consuming:** The SCAR response feeds into any client that understands kubeconfig — an MCP server, a CLI, a dashboard. The included `scar-to-kubeconfig` tool converts SCAR output into a scoped kubeconfig directly.

## Components

| Path | Description |
|------|-------------|
| `cmd/server` | Main binary. Runs the RBAC indexer + SCAR HTTP endpoint. |
| `cmd/scar-to-kubeconfig` | Helper that calls SCAR and writes a scoped kubeconfig. |
| `pkg/graph` | In-memory permission graph. No kcp imports — cleanly extractable. |
| `pkg/rbacprovider` | Watches CRBs/RBs via multicluster-runtime, translates into graph grants. |
| `pkg/virtual/scar` | SCAR HTTP handler. Reads from the graph. |
| `pkg/virtual/auth` | Auth resolver chain: bearer token (TokenReview), client cert, trusted headers. |
| `pkg/apis/access/v1alpha1` | `SelfClusterAccessReview` API types. |
| `config/apiexport` | kcp APIExport + APIResourceSchema manifests for `access.kcp.io`. |
| `config/deployment` | Kubernetes Deployment manifest for the controller. |
| `config/examples` | Example APIBinding for consumer workspaces to opt in. |
| `config/rbac` | Per-user RBAC seed files (alice=cluster-admin, bob=view+workspace-user). |
| `hack/kind/` | Kind-based full-stack setup (Makefile, manifests, helm values, scripts). |
| `hack/kind/manifests/` | Plain K8s manifests organized by component, applied via `kubectl apply -k`. |
| `hack/kind/helm/` | Helm values files for chart installations (Envoy, Keycloak, MCP server). |
| `docs/` | Testing guides, upstream contribution notes. |

## Quick start

### Prerequisites

- Go 1.25+
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

# 4. Start the server (trusted headers mode)
make run-access-vw


The demo walks through authorized users, unauthorized users, group access, and dynamic grant/revoke — with pass/fail assertions for each scenario.

See [`docs/local-testing.md`](docs/local-testing.md) for the full walkthrough.

### Kind-based setup (full stack)

Deploys the complete ADR 007 architecture into a local Kind cluster — kcp (single shard, multi-shard code path), Envoy AI Gateway (HTTPS), Keycloak (OIDC), access-vw, and kubernetes-mcp-server:

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
# 1. Start with bearer-token auth (no trusted headers)
make run-access-vw-tokenauth

# 2. Generate a scoped kubeconfig from SCAR
make mcp-demo

# 3. Run the MCP server
kubernetes-mcp-server --kubeconfig=scar.kubeconfig --cluster-provider=kcp

# 4. Connect your MCP client (Copilot CLI, Claude Code, etc.)
```

### Cleanup

```sh
make cleanup         # removes RBAC, test workspace, and APIExport (local kcp)
make kind-teardown   # deletes the Kind cluster
```

## SCAR API

**Endpoint:** `POST /services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews`

**Request:** Bearer token in `Authorization` header, or `X-Remote-User` / `X-Remote-Group` headers when behind a front-proxy.

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
curl -s http://localhost:9099/debug/graph | jq
```

Returns the current graph state: all subjects and their cluster mappings.

## Architecture

The server supports two run modes:

- **Multi-shard** (`-kubeconfig` + `-apiexport-endpointslice`): Production mode. Uses the kcp apiexport provider via multicluster-runtime to watch RBAC bindings across all workspaces bound to the `access.kcp.io` APIExport. Only workspaces with an APIBinding for `access.kcp.io` are indexed — this is the opt-in design.
- **Single-shard** (`-kubeconfig` only): Development mode. Standard client-go informers against one cluster.

Authentication chain (in order):
1. **Bearer token** — validated via `TokenReview` against kcp
2. **Client certificate** — validated against a CA pool (if configured)
3. **Trusted headers** — `X-Remote-User` / `X-Remote-Group` (only when `-trust-headers` is set, behind a front-proxy)

The `-trust-headers` flag exists because access-vw serves two traffic paths:

- **SCAR via front-proxy** — kcp's front-proxy authenticates the user and sets `X-Remote-User` / `X-Remote-Group` headers. access-vw trusts these headers without re-validating.
- **MCP via AI Gateway** — the Envoy AI Gateway forwards the raw `Authorization: Bearer` token. access-vw validates it via `TokenReview` against kcp.

> **Security note:** Trusting `X-Remote-User` headers is only safe when access-vw is not directly reachable by end users. In the current deployment, this is ensured by Kubernetes network policy (only front-proxy can reach the SCAR endpoint). For production hardening, consider replacing `-trust-headers` with client certificate verification (`-requestheader-client-ca-file`), which validates that the caller presenting `X-Remote-` headers holds a certificate signed by a trusted CA — the same pattern used by the Kubernetes API server aggregation layer.

## Deployment

See [`config/README.md`](config/README.md) for production deployment instructions covering:
1. System APIExport installation
2. Controller deployment
3. Consumer workspace opt-in (APIBinding)
4. Front-proxy routing

## Status

> **Proof of concept — SCAR is working end-to-end.** The Kind setup demonstrates the full ADR 007 architecture with a single-shard kcp deployment running the multi-shard code path (`-apiexport-endpointslice`): OIDC authentication via Keycloak, MCP routing via Envoy AI Gateway (HTTPS), per-user workspace scoping via SCAR, and RBAC indexing via the APIExport provider. Expect APIs and package layout to evolve.

## License

See [LICENSE](LICENSE).
