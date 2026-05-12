# kcp-access-vw — common development targets.
#
# Default values assume a local kcp launched with `kcp start` from
# this checkout. Override on the command line, e.g.
#   make run-kcp KUBECONFIG=/path/to/admin.kubeconfig

KUBECONFIG       ?= $(HOME)/.kcp/admin.kubeconfig
ENDPOINT_BASE    ?= https://localhost:6443/clusters/
ADDR             ?= :8080
APIEXPORT_SLICE  ?= access.kcp.io
EXPORT_PATH      ?= root
TEST_WORKSPACE   ?= test-workspace

SCAR_URL := http://localhost$(ADDR)/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ── Build & test ─────────────────────────────────────────────────────

.PHONY: build
build: ## Build the access-vw binary into bin/
	go build -o bin/access-vw ./cmd/server

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

# ── Demo / standalone ────────────────────────────────────────────────

.PHONY: run-demo
run-demo: build ## Run in demo mode (no kcp; static demo data; header auth)
	./bin/access-vw -addr $(ADDR)

# ── kcp setup ────────────────────────────────────────────────────────
#
# Run these against the workspace where the access VW's APIExport
# should live (usually root or a system-adjacent workspace).

.PHONY: install-apiexport
install-apiexport: ## Install the access.kcp.io APIExport + ARS in $(EXPORT_PATH)
	kubectl --kubeconfig=$(KUBECONFIG) ws use $(EXPORT_PATH)
	kubectl --kubeconfig=$(KUBECONFIG) apply -f config/apiexport/apiresourceschema.yaml
	kubectl --kubeconfig=$(KUBECONFIG) apply -f config/apiexport/apiexport.yaml

.PHONY: uninstall-apiexport
uninstall-apiexport: ## Remove the access.kcp.io APIExport + ARS from $(EXPORT_PATH)
	kubectl --kubeconfig=$(KUBECONFIG) ws use $(EXPORT_PATH)
	-kubectl --kubeconfig=$(KUBECONFIG) delete -f config/apiexport/apiexport.yaml
	-kubectl --kubeconfig=$(KUBECONFIG) delete -f config/apiexport/apiresourceschema.yaml

.PHONY: show-apiexport
show-apiexport: ## Show the APIExport, ARS and generated EndpointSlice
	kubectl --kubeconfig=$(KUBECONFIG) ws use $(EXPORT_PATH)
	kubectl --kubeconfig=$(KUBECONFIG) get apiexports.apis.kcp.io
	kubectl --kubeconfig=$(KUBECONFIG) get apiresourceschemas.apis.kcp.io
	kubectl --kubeconfig=$(KUBECONFIG) get apiexportendpointslices.apis.kcp.io 2>/dev/null || true

# ── Test workspace setup ─────────────────────────────────────────────

.PHONY: create-test-workspace
create-test-workspace: ## Create $(TEST_WORKSPACE) under $(EXPORT_PATH) and bind the APIExport
	kubectl --kubeconfig=$(KUBECONFIG) ws use $(EXPORT_PATH)
	-kubectl --kubeconfig=$(KUBECONFIG) ws create $(TEST_WORKSPACE)
	kubectl --kubeconfig=$(KUBECONFIG) ws use $(TEST_WORKSPACE)
	kubectl --kubeconfig=$(KUBECONFIG) apply -f config/examples/apibinding-consumer.yaml

.PHONY: delete-test-workspace
delete-test-workspace: ## Delete $(TEST_WORKSPACE) (and its bindings/RBAC)
	kubectl --kubeconfig=$(KUBECONFIG) ws use $(EXPORT_PATH)
	-kubectl --kubeconfig=$(KUBECONFIG) ws delete $(TEST_WORKSPACE)

.PHONY: seed-rbac
seed-rbac: ## Apply sample CRBs (alice / eng / platform) to the current workspace
	kubectl --kubeconfig=$(KUBECONFIG) ws use $(TEST_WORKSPACE)
	kubectl --kubeconfig=$(KUBECONFIG) apply -f hack/seed-rbac.yaml

.PHONY: unseed-rbac
unseed-rbac: ## Remove the sample CRBs from $(TEST_WORKSPACE)
	kubectl --kubeconfig=$(KUBECONFIG) ws use $(TEST_WORKSPACE)
	-kubectl --kubeconfig=$(KUBECONFIG) delete -f hack/seed-rbac.yaml

# ── Run against kcp ──────────────────────────────────────────────────

.PHONY: run-kcp
run-kcp: build ## Run against kcp in multi-shard mode (uses APIExport)
	./bin/access-vw \
		-addr $(ADDR) \
		-kubeconfig $(KUBECONFIG) \
		-apiexport-endpointslice $(APIEXPORT_SLICE) \
		-endpoint-base $(ENDPOINT_BASE) \
		-trust-headers

.PHONY: run-kcp-single
run-kcp-single: build ## Run against kcp in single-shard mode (client-go informers)
	./bin/access-vw \
		-addr $(ADDR) \
		-kubeconfig $(KUBECONFIG) \
		-endpoint-base $(ENDPOINT_BASE) \
		-trust-headers

# ── Smoke tests ──────────────────────────────────────────────────────

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
