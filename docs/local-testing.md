# Testing the Access VW against a local kcp

This walks through running the Access VW against a local kcp and verifying SCAR responses end-to-end. It's the dev inner loop; for the production deployment story see [`config/README.md`](../config/README.md).

The common operations are wrapped in the `Makefile` — `make help` lists them. The narrative below explains what each step does and what to look for when something is off.

## Prerequisites

- `go` 1.25+ (the toolchain declared in `go.mod`)
- `kcp` running locally — typically via `kcp start` from a kcp checkout; the admin kubeconfig lands at `~/.kcp/admin.kubeconfig`
- `kubectl` with the `kubectl-ws` plugin (`go install github.com/kcp-dev/kcp/cmd/kubectl-kcp/...` or the `krew` plugin)
- `jq` for prettifying SCAR responses
- (optional) a real Bearer-token issuer if you want to exercise the `TokenReviewResolver` path; for the basic flow `-trust-headers` is enough

If your kcp lives somewhere else, set `KUBECONFIG=/path/to/kubeconfig` in your shell — every `make` target below reads it from the environment.

## Recommended flow

The shape is: install the APIExport once, start the server early against an empty graph, then create the test workspace and seed RBAC while watching the server's logs. That way you see clusters engage and grants flow through reactively, which is both more demonstrative and easier to debug when something doesn't line up.

## 1. Install the system APIExport

The Access VW only indexes workspaces that opt in by binding the `access.kcp.io` APIExport. Install it once in `root` (or another system-adjacent workspace; the `EXPORT_PATH` make variable controls which):

```sh
make install-apiexport
```

Verify:

```sh
make show-apiexport
```

You should see one `APIExport` named `access.kcp.io`, one `APIResourceSchema` (`v1alpha1.selfclusteraccessreviews.access.kcp.io`), and — within a few seconds — a generated `APIExportEndpointSlice` named `access.kcp.io`. That slice name is what `-apiexport-endpointslice` points at. If you skip ahead and try `make run-kcp` before the slice exists, the server will exit immediately with a "construct apiexport provider" error; wait a moment and retry.

## 2. Build and run the Access VW

```sh
make run-kcp
```

This runs the binary in multi-shard mode (multicluster-runtime backed by the apiexport provider) with `-trust-headers` enabled so curl can authenticate by setting headers. Leave this terminal running and watch the logs — you'll see them tick over as you do the next steps.

You should see, in order:

1. `rbacprovider running multi-shard ...` from `cmd/server`.
2. `access-vw listening on :8080`.
3. Manager startup messages from controller-runtime.
4. `access graph marked ready (multicluster manager started)` from `runMulticluster`.

At this point the graph is ready but empty — there are no consumer workspaces yet. `/healthz` returns 200:

```sh
make healthz
```

And SCAR returns an empty result for any caller:

```sh
make scar-alice
# {"kind": "SelfClusterAccessReview", "apiVersion": "access.kcp.io/v1alpha1", "status": {"clusters": []}}
```

That's the right starting state — every step from here adds real data.

> **⚠ Header trust:** `-trust-headers` is only safe when nothing untrusted can reach `:8080`. The flag is gated behind an explicit opt-in for that reason. Don't expose this port without FrontProxy or a real auth layer in front.

## 3. Create a test workspace and bind it

In another terminal:

```sh
make create-test-workspace
```

This creates `test-workspace` under the export path, switches into it, and applies `config/examples/apibinding-consumer.yaml`. The binding is the explicit "yes, index me" signal — without it the workspace stays invisible.

Switch back to the terminal running the access-vw and watch the logs. You should see the apiexport provider engage a new cluster: a line about a cluster being added to the manager, then reconcile-loop chatter as the controllers start their per-cluster informer caches. The graph itself is still empty — the workspace has no RBAC yet.

Confirm the binding is `Ready`:

```sh
kubectl --kubeconfig=$KUBECONFIG ws use test-workspace
kubectl --kubeconfig=$KUBECONFIG get apibindings access.kcp.io -o yaml | grep -A1 phase
```

If it sits in `Binding` for more than a few seconds, the most common cause is that the consumer didn't accept the permission claims — the manifest already does so, but a manual edit could leave one unaccepted.

## 4. Seed some RBAC

```sh
make seed-rbac
```

This applies four `ClusterRoleBindings` to the test workspace: a user (`alice`), two groups (`eng`, `platform`), and a service account (`default:test-sa`). They all reference the standard `view` `ClusterRole`. In the current MVP the Access VW doesn't inspect role verbs — any binding counts — so the role choice doesn't matter; the bindings just have to exist.

Back in the access-vw terminal, each new CRB should produce a reconcile event. With four bindings, you'll see four reconciles touch the translator and Grant on the graph.

## 5. Query SCAR

```sh
make scar-alice
```

Now you should see something like:

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

`clusterName` is the logical-cluster identifier of the test workspace; `endpoint` is the FrontProxy URL the consumer's clients should use to reach it.

Try the group path:

```sh
make scar-eng       # anyone in group eng
make scar-multi     # alice in eng AND platform
```

A user with no matching binding returns an empty `clusters` array:

```sh
curl -sf -X POST -H 'X-Remote-User: nobody' \
  http://localhost:8080/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews | jq
```

## 6. Watch updates propagate

The whole reason for running the server early is to see this loop work. Delete one of the bindings:

```sh
kubectl --kubeconfig=$KUBECONFIG ws use test-workspace
kubectl --kubeconfig=$KUBECONFIG delete clusterrolebinding access-vw-test--alice-viewer
```

Within a second or two, `make scar-alice` should stop returning the workspace (alice still has indirect access if she's in `eng` or `platform`; otherwise the response is empty). You'll see a reconcile event in the access-vw logs and the corresponding `Revoke` on the translator.

Delete the consumer's `APIBinding` and the workspace disappears for everyone — that's the opt-in mechanism working in reverse.

> **Known gap:** the current MVP doesn't explicitly handle cluster *disengagement*. When you delete the `APIBinding` (or the entire workspace), the apiexport provider drops the cluster from the fleet, but the translator's refs and the graph's endpoints for that cluster don't get cleaned up automatically. Stale entries can linger until the next access-vw restart. Fixing this is the next concrete item on the TODO list.

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

## Optional: Bearer-token auth

To exercise the `TokenReviewResolver` rather than headers, drop `-trust-headers` from `run-kcp` (run the binary directly without the make target) and POST with `Authorization: Bearer <token>`:

```sh
TOKEN=$(kubectl --kubeconfig=$KUBECONFIG create token --duration=1h test-sa)

curl -sf -X POST \
  -H "Authorization: Bearer $TOKEN" \
  http://localhost:8080/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews | jq
```

The Access VW will call `TokenReview` against kcp using its own kubeconfig credentials and use the returned `User.Username` / `User.Groups` as the SCAR identity.

## Cleanup

```sh
make unseed-rbac          # remove sample CRBs
make delete-test-workspace
make uninstall-apiexport
```

## Common issues

- **`make run-kcp` exits with "construct apiexport provider: ..."** — usually a missing `APIExportEndpointSlice`. kcp generates the slice asynchronously after `make install-apiexport`; wait a few seconds and `make show-apiexport` until the slice is listed.
- **SCAR returns 503 forever** — the multicluster manager hasn't marked the graph ready. Check the binary's logs for cache-sync errors.
- **SCAR returns empty `clusters` for a user who should see workspaces** — the workspace probably hasn't accepted permission claims, so the controller can't see its RBAC. `kubectl get apibindings access.kcp.io -o yaml | grep -B1 -A3 permissionClaims` in the consumer workspace.
- **`clusterName` is the workspace logical cluster (e.g. `abc12def-...`), not the readable name** — by design. The endpoint URL is what consumers actually need; the readable workspace name isn't part of the SCAR contract.
- **Stale clusters in SCAR responses after deleting an `APIBinding`** — see the cluster-disengagement gap above; restart the access-vw to clear.
