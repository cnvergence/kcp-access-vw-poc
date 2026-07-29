# Default values assume a local kcp launched with `kcp start` from
# this checkout. Override on the command line, e.g.
#   make run-access-vw KUBECONFIG=/path/to/admin.kubeconfig

KUBECONFIG       ?= $(HOME)/.kcp/admin.kubeconfig
ENDPOINT_BASE    ?= https://localhost:6443/clusters/
SECURE_PORT      ?= 9443
APIEXPORT_SLICE  ?= access.kcp.io
EXPORT_PATH      ?= root
WORKSPACE         ?= root:access
CREATE_WORKSPACES ?= false
WS_ALICE         ?= workspace-alice
WS_BOB           ?= workspace-bob

SCAR_URL = https://localhost:$(SECURE_PORT)/services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews

# Bearer token for smoke tests; defaults to the kcp-admin token from the
# local kcp admin kubeconfig. Override TOKEN for other identities.
TOKEN ?= $(shell kubectl --kubeconfig $(KUBECONFIG) config view --raw \
	-o jsonpath='{.users[?(@.name=="kcp-admin")].user.token}' 2>/dev/null)

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

#Build & test

.PHONY: build
build: ## Build the access-vw and scar-to-kubeconfig binaries into bin/
	go build -o bin/access-vw ./cmd/server
	go build -o bin/access-vw-init ./cmd/init
	go build -o bin/scar-to-kubeconfig ./cmd/scar-to-kubeconfig

.PHONY: test
test: ## Run unit tests
	go test ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: tidy
tidy: ## Sync go.mod / go.sum
	go mod tidy

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin/

#kcp setup
# Run these against the workspace where the access VW's APIExport
# should live (usually root or a system-adjacent workspace).

.PHONY: init
init: build ## Bootstrap kcp: install APIExport + schema + endpoint slice, then verify
	./bin/access-vw-init \
		--kubeconfig $(KUBECONFIG) \
		--workspace $(WORKSPACE) \
		$(if $(filter true,$(CREATE_WORKSPACES)),--create-workspaces,)

.PHONY: verify-fork-pin
verify-fork-pin: ## Check the kcp Kubernetes fork pin matches virtual-workspace-framework
	./hack/verify-fork-pin.sh

.PHONY: install-apiexport
install-apiexport: ## Install the access.kcp.io APIExport + ARS in $(EXPORT_PATH)
	kubectl ws use $(EXPORT_PATH)
	kubectl apply -f config/apiexport/apiresourceschema.yaml
	kubectl apply -f config/apiexport/apiexport.yaml
	kubectl apply -f config/apiexport/apiexportendpointslice.yaml

.PHONY: show-apiexport
show-apiexport: ## Show the APIExport, ARS and generated EndpointSlice
	kubectl ws use $(EXPORT_PATH)
	kubectl get apiexports.apis.kcp.io
	kubectl get apiresourceschemas.apis.kcp.io
	kubectl get apiexportendpointslices.apis.kcp.io 2>/dev/null || true

#Test workspace setup

.PHONY: create-test-workspaces
create-test-workspaces: ## Create workspace-alice and workspace-bob, bind APIExport in each
	kubectl ws use $(EXPORT_PATH)
	-kubectl ws create $(WS_ALICE)
	kubectl ws use $(WS_ALICE)
	kubectl apply -f config/examples/apibinding-consumer.yaml
	kubectl ws use $(EXPORT_PATH)
	-kubectl ws create $(WS_BOB)
	kubectl ws use $(WS_BOB)
	kubectl apply -f config/examples/apibinding-consumer.yaml
	kubectl ws use $(EXPORT_PATH)

.PHONY: seed-rbac
seed-rbac: ## Seed RBAC: alice-sa in workspace-alice, bob-sa in workspace-bob
	kubectl ws use $(EXPORT_PATH)/$(WS_ALICE)
	kubectl apply -f config/rbac/seed-rbac-alice.yaml
	kubectl ws use $(EXPORT_PATH)/$(WS_BOB)
	kubectl apply -f config/rbac/seed-rbac-bob.yaml
	kubectl ws use $(EXPORT_PATH)


.PHONY: cleanup
cleanup: ## Remove all test resources: RBAC, workspaces, APIExport
	-kubectl ws use $(EXPORT_PATH)/$(WS_ALICE) && kubectl delete -f config/rbac/seed-rbac-alice.yaml
	-kubectl ws use $(EXPORT_PATH)/$(WS_BOB) && kubectl delete -f config/rbac/seed-rbac-bob.yaml
	-kubectl ws use $(EXPORT_PATH) && kubectl ws delete $(WS_ALICE)
	-kubectl ws use $(EXPORT_PATH) && kubectl ws delete $(WS_BOB)
	-kubectl ws use $(EXPORT_PATH) && kubectl delete -f config/apiexport/apiexport.yaml
	-kubectl ws use $(EXPORT_PATH) && kubectl delete -f config/apiexport/apiresourceschema.yaml
	kubectl ws use $(EXPORT_PATH)

# ── Run against kcp ──────────────────────────────────────────────────

.PHONY: run-access-vw
run-access-vw: build ## Run against kcp (TLS on $(SECURE_PORT), bearer tokens via TokenReview; self-signs a dev cert)
	./bin/access-vw \
		--secure-port $(SECURE_PORT) \
		--kubeconfig $(KUBECONFIG) \
		--apiexport-endpointslice $(APIEXPORT_SLICE) \
		--endpoint-base $(ENDPOINT_BASE)

#Smoke tests
# The server validates bearer tokens via TokenReview against kcp;
# identity comes from the token (no header spoofing). Override TOKEN
# to test other identities (e.g. a ServiceAccount token).

.PHONY: scar
scar: ## Issue a SCAR as the caller identified by $(TOKEN)
	@curl -ksf -X POST \
		-H "Authorization: Bearer $(TOKEN)" \
		-H 'Content-Type: application/json' -d '{}' \
		$(SCAR_URL) | jq

.PHONY: debug-graph
debug-graph: ## Dump the access graph (authenticated)
	@curl -ksf -H "Authorization: Bearer $(TOKEN)" \
		https://localhost:$(SECURE_PORT)/debug/graph | jq

.PHONY: healthz
healthz: ## Hit /healthz
	@curl -ksf https://localhost:$(SECURE_PORT)/healthz; echo

#MCP demo

.PHONY: mcp-demo
mcp-demo: build ## Generate scoped alice.kubeconfig for MCP demo
	@echo "==> Generating alice.kubeconfig (workspace-alice only)..."; \
	kubectl ws use $(EXPORT_PATH)/$(WS_ALICE) >/dev/null 2>&1 || true; \
	TOKEN_VAL=$$(kubectl create token alice-sa --namespace=default --duration=1h 2>/dev/null); \
	if [ -z "$$TOKEN_VAL" ]; then \
		echo "error: could not obtain token for alice-sa. Run 'make seed-rbac' first." >&2; \
		exit 1; \
	fi; \
	./bin/scar-to-kubeconfig -scar-url "$(SCAR_URL)" -token "$$TOKEN_VAL" -insecure -output alice.kubeconfig
	@kubectl ws use $(EXPORT_PATH) >/dev/null 2>&1 || true
	@echo ""
	@echo "Kubeconfig ready:"
	@echo "  alice.kubeconfig → can access workspace-alice (create/list workspaces, view resources)"
	@echo ""
	@echo "Run MCP server:"
	@echo "  kubernetes-mcp-server --kubeconfig=$(CURDIR)/alice.kubeconfig --cluster-provider=kcp --toolsets=core,kcp --port 8080"
	@echo ""
	@echo "Or add to ~/.copilot/mcp-config.json:"
	@echo '  {"mcpServers":{"kcp-access":{"type":"local","command":"kubernetes-mcp-server","args":["--kubeconfig","$(CURDIR)/alice.kubeconfig","--cluster-provider=kcp","--toolsets=core,kcp"]}}}'

#Kind-based setup
.PHONY: kind-setup
kind-setup: ## Create Kind cluster with full kcp + MCP stack
	$(MAKE) -C hack/kind setup

.PHONY: kind-teardown
kind-teardown: ## Delete the Kind cluster
	$(MAKE) -C hack/kind teardown

.PHONY: kind-build
kind-build: docker-build ## Build access-vw image and load into Kind
	kind load docker-image localhost/access-vw:local --name kcp-access-vw

.PHONY: docker-build
docker-build: ## Build access-vw Docker image
	docker build -t localhost/access-vw:local .
