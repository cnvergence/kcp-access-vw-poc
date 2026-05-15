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
make create-test-workspace
make seed-rbac

# 4. Start the server (trusted headers mode)
make run-access-vw

# 5. Query SCAR (in another terminal)
curl -s -X POST -H 'X-Remote-User: alice' \
  http://localhost:9099/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews | jq
```

See [`docs/local-testing.md`](docs/local-testing.md) for the full walkthrough.

### Kind-based setup (full stack)

Deploys the complete ADR 007 architecture into a local Kind cluster — kcp, Envoy AI Gateway, Keycloak (OIDC), access-vw, and kubernetes-mcp-server:

```sh
make kind-setup     # ~5 min, creates everything
make kind-teardown  # delete the cluster
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

- **Multi-shard** (`-kubeconfig` + `-apiexport-endpointslice`): Production mode. Uses the kcp apiexport provider via multicluster-runtime to watch bindings across all workspaces bound to the `access.kcp.io` APIExport.
- **Single-shard** (`-kubeconfig` only): Development mode. Standard client-go informers against one cluster.

Authentication chain (in order):
1. **Bearer token** — validated via `TokenReview` against kcp
2. **Client certificate** — validated against a CA pool (if configured)
3. **Trusted headers** — `X-Remote-User` / `X-Remote-Group` (only when `-trust-headers` is set, behind a front-proxy)

## Deployment

See [`config/README.md`](config/README.md) for production deployment instructions covering:
1. System APIExport installation
2. Controller deployment
3. Consumer workspace opt-in (APIBinding)
4. Front-proxy routing

## Status

> **Proof of concept.** The architecture and decisions are tracked in ADR 007. Expect APIs and package layout to evolve.

## License

See [LICENSE](LICENSE).
