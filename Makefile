# Default values assume a local kcp launched with `kcp start` from
# this checkout. Override on the command line, e.g.
#   make run-access-vw KUBECONFIG=/path/to/admin.kubeconfig

KUBECONFIG       ?= $(HOME)/.kcp/admin.kubeconfig
ENDPOINT_BASE    ?= https://localhost:6443/clusters/
ADDR             ?= :9099
APIEXPORT_SLICE  ?= access.kcp.io
EXPORT_PATH      ?= root
WS_ALICE         ?= workspace-alice
WS_BOB           ?= workspace-bob

SCAR_URL = http://localhost$(ADDR)/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

#Build & test

.PHONY: build
build: ## Build the access-vw and scar-to-kubeconfig binaries into bin/
	go build -o bin/access-vw ./cmd/server
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

.PHONY: install-apiexport
install-apiexport: ## Install the access.kcp.io APIExport + ARS in $(EXPORT_PATH)
	kubectl ws use $(EXPORT_PATH)
	kubectl apply -f config/apiexport/apiresourceschema.yaml
	kubectl apply -f config/apiexport/apiexport.yaml

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
run-access-vw: build ## Run against kcp with trusted headers (for smoke tests with X-Remote-User)
	./bin/access-vw \
		-addr $(ADDR) \
		-kubeconfig $(KUBECONFIG) \
		-apiexport-endpointslice $(APIEXPORT_SLICE) \
		-endpoint-base $(ENDPOINT_BASE) \
		-trust-headers

.PHONY: run-access-vw-tokenauth
run-access-vw-tokenauth: build ## Run against kcp with bearer-token auth (for MCP demo)
	./bin/access-vw \
		-addr $(ADDR) \
		-kubeconfig $(KUBECONFIG) \
		-apiexport-endpointslice $(APIEXPORT_SLICE) \
		-endpoint-base $(ENDPOINT_BASE)

#Smoke tests

.PHONY: scar-alice
scar-alice: ## Issue a SCAR as user=alice (requires -trust-headers)
	@curl -sf -X POST -H 'X-Remote-User: alice' $(SCAR_URL) | jq

.PHONY: scar-eng
scar-eng: ## Issue a SCAR as a user in group=eng
	@curl -sf -X POST \
		-H 'X-Remote-User: someone' \
		-H 'X-Remote-Group: eng' \
		$(SCAR_URL) | jq

.PHONY: scar-multi
scar-multi: ## Issue a SCAR as alice in groups eng+platform
	@curl -sf -X POST \
		-H 'X-Remote-User: alice' \
		-H 'X-Remote-Group: eng' \
		-H 'X-Remote-Group: platform' \
		$(SCAR_URL) | jq

.PHONY: healthz
healthz: ## Hit /healthz
	@curl -sf http://localhost$(ADDR)/healthz; echo

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
