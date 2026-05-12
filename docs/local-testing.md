# Testing the Access VW against a local kcp

This walks through running the Access VW against a local kcp and verifying SCAR responses end-to-end. It's the dev inner loop; for the production deployment story see [`config/README.md`](../config/README.md).

The common operations are wrapped in the `Makefile` — `make help` lists them. The narrative below explains what each step does and what to look for when something is off.

## Prerequisites

- `go` 1.25+ (the toolchain declared in `go.mod`)
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

You should see one `APIExport` named `access.kcp.io`, one `APIResourceSchema`, and — within a few seconds — a generated `APIExportEndpointSlice` named `access.kcp.io`. If the slice doesn't exist yet and you try `make run-kcp`, the server will exit with a "construct apiexport provider" error; wait a moment and retry.

## 2. Build and run the Access VW

```sh
make run-kcp
```

This runs in multi-shard mode with `-trust-headers` enabled so curl can authenticate by setting `X-Remote-User` / `X-Remote-Group` headers. Leave this terminal running.

You should see, in order:

1. `rbacprovider running multi-shard ...`
2. `access-vw listening on :9099`
3. Controller-runtime startup messages.
4. `access graph marked ready (multicluster manager started)`

At this point the graph is ready but empty. `/healthz` returns 200:

```sh
make healthz
```

And SCAR returns an empty result for any caller:

```sh
make scar-alice
# {"kind": "SelfClusterAccessReview", ..., "status": {"clusters": []}}
```

> **⚠ Header trust:** `-trust-headers` is only safe when nothing untrusted can reach `:9099`. Don't expose this port without a front-proxy or real auth layer in front.

## 3. Create a test workspace and bind it

In another terminal:

```sh
make create-test-workspace
```

This creates `test-workspace` under root, applies `config/examples/apibinding-consumer.yaml` (the APIBinding), and restores the kubeconfig context to root.

Watch the access-vw logs — you should see the apiexport provider engage a new cluster. The graph is still empty because the workspace has no RBAC yet.

Confirm the binding is `Ready`:

```sh
kubectl ws use ':root:test-workspace'
kubectl get apibindings access.kcp.io -o yaml | grep -A1 phase
kubectl ws use ':root'
```

## 4. Seed some RBAC

```sh
make seed-rbac
```

This applies `ClusterRoleBindings` for: user `alice`, groups `eng` and `platform`, service account `test-sa`, plus a `workspace-admin` ClusterRole granting workspace create/delete permissions. Each new CRB triggers a reconcile event in the access-vw logs.

## 5. Query SCAR

```sh
make scar-alice
```

You should see:

```json
{
  "kind": "SelfClusterAccessReview",
  "apiVersion": "access.kcp.io/v1alpha1",
  "status": {
    "clusters": [
      {
        "clusterName": "abc12def-...",
        "endpoint": "https://localhost:6443/clusters/abc12def-..."
      }
    ]
  }
}
```

Try other identities:

```sh
make scar-eng       # anyone in group eng
make scar-multi     # alice in eng AND platform
```

A user with no matching binding returns an empty `clusters` array:

```sh
curl -sf -X POST -H 'X-Remote-User: nobody' \
  http://localhost:9099/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews | jq
```

### Debug endpoint

Check the current graph state:

```sh
curl -s http://localhost:9099/debug/graph | jq
```

Returns subjects (with their cluster mappings) and clusters (with their endpoints).

## 6. Watch updates propagate

Delete one of the bindings:

```sh
kubectl ws use ':root:test-workspace'
kubectl delete clusterrolebinding access-vw-test--alice-viewer
kubectl ws use ':root'
```

Within seconds, `make scar-alice` should stop returning the workspace (unless alice is also in `eng` or `platform`). You'll see a reconcile event and `Revoke` in the logs.

## 7. Iterate

Standard inner loop:

```sh
# edit pkg/...
make build
# Ctrl-C the running access-vw
make run-kcp
```

Tests:

```sh
make test
make vet
```

## Bearer-token auth

To exercise the `TokenReviewResolver` path instead of trusted headers:

```sh
make run-kcp-tokenauth
```

Then POST with `Authorization: Bearer <token>`:

```sh
TOKEN=$(kubectl create token test-sa --namespace=default --duration=1h)

curl -sf -X POST \
  -H "Authorization: Bearer $TOKEN" \
  http://localhost:9099/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews | jq
```

> **Note:** The `scar-alice` / `scar-eng` smoke targets use `X-Remote-User` headers and only work with `make run-kcp` (trusted headers mode).

## MCP demo (manual scoping)

This proves SCAR's output is consumable by a real MCP server end-to-end. A scoped kubeconfig is generated from SCAR and fed to `kubernetes-mcp-server`, so an MCP client sees exactly the workspaces SCAR returned — no more, no less.

**Snapshot semantics:** the kubeconfig captures access at one moment. It won't reflect RBAC changes mid-session.

### Step-by-step

**1. Start the Access VW with bearer-token auth:**

```sh
make run-kcp-tokenauth
```

**2. Generate the scoped kubeconfig:**

```sh
make mcp-demo
```

This generates a token from `test-sa`, calls SCAR, and writes `scar.kubeconfig`.

**3. Run the MCP server:**

```sh
kubernetes-mcp-server --kubeconfig=scar.kubeconfig --cluster-provider=kcp
```

**4. Connect your MCP client** (e.g. Claude Code, Copilot CLI) and verify it sees only the authorized workspaces. You can list namespaces, create child workspaces, and manage resources — all scoped by SCAR.

### MCP cleanup

```sh
rm -f scar.kubeconfig
```

## Cleanup

Remove all test resources in one command:

```sh
make cleanup
```
