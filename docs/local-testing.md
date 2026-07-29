# Testing the Access VW against a local kcp

This walks through running the Access VW against a local kcp and verifying SCAR responses end-to-end. It's the dev inner loop; for the production deployment story see [`config/README.md`](../config/README.md).

The common operations are wrapped in the `Makefile` — `make help` lists them. The narrative below explains what each step does and what to look for when something is off.

## Prerequisites

- `go` 1.26+ (the toolchain declared in `go.mod`)
- `kcp` running locally — typically via `kcp start` from a kcp checkout; the admin kubeconfig lands at `~/.kcp/admin.kubeconfig`
- `kubectl` with the `kubectl-ws` plugin (`go install github.com/kcp-dev/kcp/cmd/kubectl-kcp/...` or the `krew` plugin)
- `jq` for prettifying SCAR responses
- (optional) [`kubernetes-mcp-server`](https://github.com/containers/kubernetes-mcp-server) for the MCP demo

If your kcp lives somewhere else, set `KUBECONFIG=/path/to/kubeconfig` in your shell — every `make` target reads it from the environment.

> **⚠ Kubeconfig mutation:** `kubectl ws use` writes the selected workspace's server URL back into the kubeconfig file. If you run `kubectl ws use test-workspace` and then restart access-vw, it will read the mutated kubeconfig pointing at the child workspace instead of root. The make targets handle this (they restore context to root when done), but be aware of it if you use `kubectl ws` manually. Fix with `kubectl ws use ':root'`.

## Recommended flow

Install the APIExport once, start the server early against an empty graph, then create the test workspace and seed RBAC while watching the server's logs. That way you see clusters engage and grants flow through reactively.

## 1. Install the system APIExport

The Access VW only indexes workspaces that opt in by binding the `access.kcp.io` APIExport. Install it once in `root`:

```sh
make install-apiexport
```

Verify:

```sh
make show-apiexport
```

You should see one `APIExport` named `access.kcp.io`, one `APIResourceSchema`, and — within a few seconds — a generated `APIExportEndpointSlice` named `access.kcp.io`. If the slice doesn't exist yet and you try `make run-access-vw`, the server will exit with a "construct apiexport provider" error; wait a moment and retry.

## 2. Build and run the Access VW

```sh
make run-access-vw
```

This runs in multi-shard mode, serving TLS on `:9443` with a self-signed dev cert. Callers authenticate with bearer tokens, which the server validates via TokenReview against kcp. Leave this terminal running.

You should see, in order:

1. `rbacprovider running multi-shard ...`
2. `Serving securely on [::]:9443`
3. Controller-runtime startup messages.
4. `access graph marked ready (multicluster manager started)`

At this point the graph is ready but empty. `/healthz` returns 200:

```sh
make healthz
```

And SCAR returns an empty result for any caller (`make scar` uses the kcp-admin token from your kcp admin kubeconfig by default):

```sh
make scar
# {"kind": "SelfClusterAccessReview", ..., "status": {"clusters": []}}
```

> **Note:** There is no header-trust mode anymore. `X-Remote-*` identity headers are only accepted over mTLS from a client certificate signed by `--requestheader-client-ca-file` (the front-proxy pattern); for local testing you authenticate with tokens.

## 3. Create test workspaces and bind them

In another terminal:

```sh
make create-test-workspaces
```

This creates two workspaces (`workspace-alice` and `workspace-bob`) under root, applies the `access.kcp.io` APIBinding in each, and restores the kubeconfig context to root.

Watch the access-vw logs — you should see the apiexport provider engage two new clusters. The graph is still empty because neither workspace has RBAC yet.

## 4. Seed RBAC

```sh
make seed-rbac
```

This creates:
- `alice-sa` ServiceAccount in `workspace-alice` with `view` + `workspace-user` roles (can create/list workspaces but **not** delete)
- `bob-sa` ServiceAccount in `workspace-bob` with `view` role

Each SA only has access to its own workspace. The access-vw logs will show reconcile events as each CRB is created.

## 5. Query SCAR

Identity comes from the bearer token. Test with the seeded ServiceAccounts — each should see only its own workspace:

```sh
# alice-sa → workspace-alice
kubectl ws use ':root:workspace-alice'
TOKEN=$(kubectl create token alice-sa --namespace=default --duration=1h)
kubectl ws use ':root'
make scar TOKEN=$TOKEN
```

A caller with no matching binding (e.g. the kcp-admin default) returns an empty `clusters` array:

```sh
make scar
```

### Debug endpoint

Check the current graph state (any authenticated caller):

```sh
make debug-graph
# or: curl -ks -H "Authorization: Bearer $TOKEN" https://localhost:9443/debug/graph | jq
```

Returns subjects (with their cluster mappings) and clusters (with their endpoints).

## 6. Watch updates propagate

Delete one of the bindings:

```sh
kubectl ws use ':root:workspace-alice'
kubectl delete clusterrolebinding access-vw-test--alice-sa-viewer
kubectl ws use ':root'
```

Within seconds, alice-sa's token should stop returning the workspace. You'll see a reconcile event and `Revoke` in the logs.

## 7. Iterate

Standard inner loop:

```sh
# edit pkg/...
make build
# Ctrl-C the running access-vw
make run-access-vw
```

Tests:

```sh
make test
make vet
```

## Raw curl

The equivalent of `make scar` without the Makefile:

```sh
TOKEN=$(kubectl create token test-sa --namespace=default --duration=1h)

curl -ksf -X POST \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{}' \
  https://localhost:9443/services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews | jq
```

(`-k` because the dev serving cert is self-signed.)

## MCP demo — AI agent with scoped access

This proves SCAR's output is consumable by a real MCP server end-to-end. A scoped kubeconfig is generated from SCAR and fed to `kubernetes-mcp-server`, so an MCP client (Copilot CLI, Claude Code) sees exactly the workspaces SCAR returned — no more, no less.

**Snapshot semantics:** the kubeconfig captures access at one moment. It won't reflect RBAC changes mid-session.

### Step-by-step

**1. Start the Access VW:**

```sh
make run-access-vw
```

**2. Generate the scoped kubeconfig:**

```sh
make mcp-demo
```

This obtains a token for `alice-sa`, calls SCAR, and writes `alice.kubeconfig` — containing only the workspace-alice endpoint. The output also prints the MCP server command and Copilot config.

**3. Connect your MCP client:**

Option A — run the MCP server in a separate terminal and connect via HTTP:

```sh
kubernetes-mcp-server --kubeconfig=alice.kubeconfig --cluster-provider=kcp --toolsets=core,kcp --port 8080
```

Add to `.mcp.json` (in the repo root or `~`):

```json
{
  "mcpServers": {
    "kcp-access": {
      "type": "http",
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

Option B — let Copilot CLI manage the MCP server as a local process. Add to `~/.copilot/mcp-config.json`:

```json
{
  "mcpServers": {
    "kcp-access": {
      "type": "local",
      "command": "kubernetes-mcp-server",
      "args": [
        "--kubeconfig", "/absolute/path/to/alice.kubeconfig",
        "--cluster-provider=kcp",
        "--toolsets=core,kcp"
      ]
    }
  }
}
```

**4. Start a new Copilot CLI / Claude Code session** — the MCP tools will be available automatically.

### Showcasing — what alice-sa CAN do

Once connected, try these prompts:

```
"List my kcp workspaces"
"List all namespaces"
"What resources exist in the cluster?"
"Create a workspace called test-child with type universal"
```

Alice-sa has `view` + `workspace-user` roles, so she can:
- ✅ List workspaces (sees only workspace-alice)
- ✅ List namespaces, pods, services, etc.
- ✅ Create child workspaces

### Showcasing — what alice-sa CANNOT do

```
"Delete the test-child workspace"
"Create a workspace with type organization"
```

Expected results:
- ❌ **Delete workspace** — `access denied` (the `workspace-user` role intentionally excludes `delete`)
- ❌ **Create organization workspace** — kcp rejects it (only allowed under root-type parents)
- ❌ **See workspace-bob** — the endpoint isn't in the kubeconfig at all, so there's nothing to reach

**The key insight:** SCAR doesn't just filter responses — it determines what endpoints exist in the kubeconfig. If a workspace isn't in the SCAR response, there's no URL to call. The MCP server literally cannot reach it.

### Verifying directly with curl

You can also verify the raw SCAR responses without the MCP server:

```sh
# alice-sa → returns workspace-alice endpoint
kubectl ws use root/workspace-alice
TOKEN=$(kubectl create token alice-sa --namespace=default --duration=1h)
curl -ksf -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{}' \
  https://localhost:9443/services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews | jq
# → {"status": {"clusters": [{"clusterName": "...", "endpoint": "..."}]}}

# bob-sa → returns workspace-bob endpoint (different cluster)
kubectl ws use root/workspace-bob
TOKEN=$(kubectl create token bob-sa --namespace=default --duration=1h)
curl -ksf -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" -d '{}' \
  https://localhost:9443/services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews | jq
# → {"status": {"clusters": [{"clusterName": "...", "endpoint": "..."}]}}
# (different clusterName than alice!)
```

### MCP cleanup

```sh
rm -f alice.kubeconfig
```

## Cleanup

Remove all test resources in one command:

```sh
make cleanup
```
