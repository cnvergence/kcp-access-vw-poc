# hack/kind/Makefile — Kind-based local setup for kcp-access-vw.
#
# Usage:
#   make setup           # full end-to-end setup
#   make teardown        # delete Kind cluster
#   make scar USER=alice # test SCAR for a user
#
# Prerequisites: kind, kubectl, helm, jq, curl, docker

SHELL := /bin/bash
.DEFAULT_GOAL := help

# ── Configuration ────────────────────────────────────────────────────────────

CLUSTER_NAME     ?= kcp-access-vw
KIND_KUBECONFIG  ?= $(HOME)/.kube/config
REPO_ROOT        := $(shell cd ../.. && pwd)
SCRIPT_DIR       := $(shell pwd)

# Versions
CERT_MANAGER_VERSION  ?= v1.20.2
ENVOY_GATEWAY_VERSION ?= v1.8.0
METALLB_VERSION       ?= v0.15.3

# Namespaces
KCP_NS      := kcp-system
KEYCLOAK_NS := keycloak
MCP_NS      := mcp

# User for get-token / scar targets
USER ?= alice

# kubectl against the Kind cluster
KC := kubectl --kubeconfig $(KIND_KUBECONFIG) --context kind-$(CLUSTER_NAME)

# ── Helpers ──────────────────────────────────────────────────────────────────

define wait-for-deployment
	@echo "==> Waiting for deployment $(2) in $(1)..."
	@$(KC) rollout status deployment/$(2) -n $(1) --timeout=$(or $(3),300s)
endef

define wait-for-crd
	@echo "==> Waiting for CRD $(1)..."
	@for i in $$(seq 1 60); do $(KC) get crd $(1) &>/dev/null && break || sleep 2; done
endef

info  = @echo "==> $(1)"

# ── Targets ──────────────────────────────────────────────────────────────────

.PHONY: help
help: ## Show available targets
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ── Cluster lifecycle ────────────────────────────────────────────────────────

.PHONY: kind-create
kind-create: ## Create Kind cluster
	$(call info,Creating Kind cluster '$(CLUSTER_NAME)')
	@if kind get clusters 2>/dev/null | grep -q "^$(CLUSTER_NAME)$$"; then \
		echo "⚠️  Cluster '$(CLUSTER_NAME)' already exists"; \
	else \
		kind create cluster --name $(CLUSTER_NAME) --config kind-config.yaml; \
	fi

.PHONY: teardown
teardown: ## Delete Kind cluster
	kind delete cluster --name $(CLUSTER_NAME)

# ── Infrastructure ───────────────────────────────────────────────────────────

.PHONY: install-metallb
install-metallb: ## Install MetalLB for LoadBalancer IPs
	$(call info,Installing MetalLB $(METALLB_VERSION))
	@$(KC) apply -f "https://raw.githubusercontent.com/metallb/metallb/$(METALLB_VERSION)/config/manifests/metallb-native.yaml"
	$(call wait-for-deployment,metallb-system,controller,120s)
	@$(KC) get secret -n metallb-system memberlist --no-headers --ignore-not-found -o name | \
		grep -q . || $(KC) create secret generic -n metallb-system memberlist \
		--from-literal=secretkey="$$(openssl rand -base64 128)"
	@echo "==> Waiting for MetalLB webhooks..."
	@sleep 5
	@for i in 1 2 3 4 5; do \
		$(KC) apply -k manifests/metallb 2>/dev/null && break; \
		echo "==> MetalLB webhook not ready, retrying ($$i/5)..."; sleep 5; \
	done

.PHONY: install-cert-manager
install-cert-manager: ## Install cert-manager
	$(call info,Installing cert-manager $(CERT_MANAGER_VERSION))
	@$(KC) apply -f "https://github.com/cert-manager/cert-manager/releases/download/$(CERT_MANAGER_VERSION)/cert-manager.yaml"
	@$(KC) wait --for=condition=Available deployment/cert-manager -n cert-manager --timeout=120s
	@$(KC) wait --for=condition=Available deployment/cert-manager-webhook -n cert-manager --timeout=120s
	@echo "==> Waiting for cert-manager webhook..."
	@sleep 10

.PHONY: install-envoy
install-envoy: ## [optional] Install Envoy Gateway + AI Gateway (helm)
	$(call info,Installing Envoy Gateway $(ENVOY_GATEWAY_VERSION))
	@helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm \
		--version $(ENVOY_GATEWAY_VERSION) \
		--namespace envoy-gateway-system --create-namespace \
		-f helm/envoy-gateway-values.yaml \
		--wait --timeout 120s
	$(call info,Installing Envoy AI Gateway CRDs)
	@helm upgrade --install ai-gateway-crds oci://docker.io/envoyproxy/ai-gateway-crds-helm \
		--version 0.6.0 \
		--namespace envoy-ai-gateway-system --create-namespace \
		--wait --timeout 60s
	$(call info,Installing Envoy AI Gateway controller)
	@helm upgrade --install ai-gateway oci://docker.io/envoyproxy/ai-gateway-helm \
		--version 0.6.0 \
		--namespace envoy-ai-gateway-system --create-namespace \
		--wait --timeout 120s

# ── Keycloak ─────────────────────────────────────────────────────────────────

.PHONY: install-keycloak
install-keycloak: ## Install Keycloak (helm + TLS + configure realm)
	$(call info,Creating Keycloak TLS certificate via mkcert)
	@scripts/create-keycloak-certs.sh
	$(call info,Installing Keycloak via Helm)
	@helm repo add codecentric https://codecentric.github.io/helm-charts 2>/dev/null || true
	@helm repo update codecentric
	@helm upgrade --install keycloak codecentric/keycloakx \
		--namespace $(KEYCLOAK_NS) \
		-f helm/keycloak-values.yaml \
		--wait --timeout 300s
	$(call info,Configuring Keycloak OIDC clients and users)
	@scripts/configure-keycloak.sh

# ── kcp ──────────────────────────────────────────────────────────────────────

.PHONY: install-kcp
install-kcp: ## Install kcp-operator + etcd + RootShard + FrontProxy
	$(call info,Deploying kcp-operator)
	@$(KC) create namespace $(KCP_NS) --dry-run=client -o yaml | $(KC) apply -f -
	@$(KC) apply -k manifests/kcp-operator
	$(call wait-for-deployment,kcp-operator-system,kcp-operator-controller-manager,120s)
	# Apply kcp manifests (issuer, etcd, root-shard, front-proxy, kubeconfigs, nodeport)
	$(call wait-for-crd,rootshards.operator.kcp.io)
	$(call wait-for-crd,frontproxies.operator.kcp.io)
	$(call info,Applying kcp manifests)
	@$(KC) apply -k manifests/kcp
	$(call wait-for-deployment,$(KCP_NS),etcd,120s)
	$(call info,Waiting for RootShard)
	@$(KC) wait --for=condition=Available rootshard/root -n $(KCP_NS) --timeout=300s
	$(call info,Waiting for FrontProxy)
	@$(KC) wait --for=condition=Available frontproxy/frontproxy -n $(KCP_NS) --timeout=300s
	# Patch RootShard with hostAliases so kcp pods can reach root.kcp.example and keycloak.kcp.example
	$(call info,Patching RootShard hostAliases)
	@FP_IP=$$($(KC) get svc frontproxy-front-proxy -n $(KCP_NS) -o jsonpath='{.spec.clusterIP}'); \
	KC_IP=$$($(KC) get svc keycloak-keycloakx-http -n keycloak -o jsonpath='{.spec.clusterIP}'); \
	$(KC) patch rootshard root -n $(KCP_NS) --type merge -p " \
	  {\"spec\":{\"deploymentTemplate\":{\"spec\":{\"template\":{\"spec\":{\"hostAliases\":[{\"ip\":\"$$FP_IP\",\"hostnames\":[\"root.kcp.example\"]},{\"ip\":\"$$KC_IP\",\"hostnames\":[\"keycloak.kcp.example\"]}]}}}}}}"
	# Patch FrontProxy with hostAliases so it can reach keycloak.kcp.example for OIDC
	$(call info,Patching FrontProxy hostAliases)
	@KC_IP=$$($(KC) get svc keycloak-keycloakx-http -n keycloak -o jsonpath='{.spec.clusterIP}'); \
	$(KC) patch frontproxy frontproxy -n $(KCP_NS) --type merge -p " \
	  {\"spec\":{\"deploymentTemplate\":{\"spec\":{\"template\":{\"spec\":{\"hostAliases\":[{\"ip\":\"$$KC_IP\",\"hostnames\":[\"keycloak.kcp.example\"]}]}}}}}}"
	$(call wait-for-deployment,$(KCP_NS),root-kcp,120s)
	# Restart kcp components to pick up OIDC (Keycloak must be deployed first)
	$(call info,Restarting kcp for OIDC discovery)
	@$(KC) rollout restart deployment/root-kcp -n $(KCP_NS)
	@$(KC) rollout restart deployment/frontproxy-front-proxy -n $(KCP_NS)
	$(call wait-for-deployment,$(KCP_NS),root-kcp,120s)
	$(call wait-for-deployment,$(KCP_NS),frontproxy-front-proxy,120s)
	# Extract admin kubeconfig
	$(call info,Extracting admin kubeconfig)
	@$(KC) wait --for=jsonpath='{.status.phase}'=Ready kubeconfig/kcp-admin-kubeconfig \
		-n $(KCP_NS) --timeout=120s
	@$(KC) get secret kcp-admin-kubeconfig -n $(KCP_NS) \
		-o jsonpath='{.data.kubeconfig}' | base64 -d > admin.kubeconfig
	@sed -i.bak 's|https://root-kcp.kcp-system.svc.cluster.local:6443|https://root.kcp.example:6443|g' admin.kubeconfig
	@rm -f admin.kubeconfig.bak
	@KUBECONFIG=admin.kubeconfig kubectl config set-cluster base --insecure-skip-tls-verify=true 2>/dev/null || true
	@KUBECONFIG=admin.kubeconfig kubectl config set-cluster default --insecure-skip-tls-verify=true 2>/dev/null || true
	$(call info,Admin kubeconfig saved to $(SCRIPT_DIR)/admin.kubeconfig)

# ── access-vw ────────────────────────────────────────────────────────────────

.PHONY: build-image
build-image: ## Build access-vw image and load into Kind
	$(call info,Building access-vw Docker image)
	@cd $(REPO_ROOT) && docker build -t localhost/access-vw:local .
	@kind load docker-image localhost/access-vw:local --name $(CLUSTER_NAME)

.PHONY: install-access-vw
install-access-vw: build-image ## Build image + deploy access-vw
	$(call info,Deploying access-vw)
	@$(KC) apply -k manifests/access-vw
	@$(KC) wait --for=jsonpath='{.status.phase}'=Ready kubeconfig/access-vw-kubeconfig \
		-n $(KCP_NS) --timeout=120s
	# Patch access-vw with hostAliases so it can reach root.kcp.example
	$(call info,Patching access-vw hostAliases for FrontProxy and Keycloak)
	@FP_IP=$$($(KC) get svc frontproxy-front-proxy -n $(KCP_NS) -o jsonpath='{.spec.clusterIP}'); \
	KC_IP=$$($(KC) get svc keycloak-keycloakx-http -n $(KEYCLOAK_NS) -o jsonpath='{.spec.clusterIP}'); \
	$(KC) patch deployment access-vw -n $(KCP_NS) --type merge -p " \
	  {\"spec\":{\"template\":{\"spec\":{\"hostAliases\":[{\"ip\":\"$$FP_IP\",\"hostnames\":[\"root.kcp.example\"]},{\"ip\":\"$$KC_IP\",\"hostnames\":[\"keycloak.kcp.example\"]}]}}}}"
	$(call wait-for-deployment,$(KCP_NS),access-vw,120s)

# ── MCP server + AI Gateway route ───────────────────────────────────────────

.PHONY: install-mcp
install-mcp: ## [optional] Install MCPRoute (via kcp front-proxy) + Gateway
	$(call info,Publishing kcp root CA for Envoy backend TLS validation)
	@$(KC) get secret root-ca -n $(KCP_NS) -o jsonpath='{.data.tls\.crt}' | base64 -d > /tmp/kcp-root-ca.crt; \
	$(KC) create configmap kcp-root-ca -n $(KCP_NS) \
		--from-file=ca.crt=/tmp/kcp-root-ca.crt \
		--dry-run=client -o yaml | $(KC) apply -f -; \
	rm -f /tmp/kcp-root-ca.crt
	$(call info,Installing MCP Gateway and Route)
	@$(KC) apply -k manifests/ai-gateway

.PHONY: install-mcp-upstream
install-mcp-upstream: ## Install upstream kubernetes-mcp-server as alternative MCP backend
	$(call info,Deploying kubernetes-mcp-server kubeconfig CR)
	@$(KC) apply -k manifests/mcp-server
	@$(KC) wait --for=jsonpath='{.status.phase}'=Ready kubeconfig/mcp-server-kubeconfig \
		-n $(KCP_NS) --timeout=120s
	$(call info,Installing kubernetes-mcp-server via Helm)
	@helm upgrade --install mcp-server \
		oci://ghcr.io/containers/charts/kubernetes-mcp-server \
		--namespace $(KCP_NS) \
		-f helm/mcp-values.yaml \
		--wait --timeout 120s
	$(call info,kubernetes-mcp-server deployed — update MCPRoute backendRef to point to it)
	@echo "  To use this instead of the built-in MCP, update mcp-route.yaml backendRef:"
	@echo "    name: mcp-server-kubernetes-mcp-server"
	@echo "    port: 8080"
	@echo "    path: /mcp"

.PHONY: uninstall-mcp-upstream
uninstall-mcp-upstream: ## Remove upstream kubernetes-mcp-server
	$(call info,Removing kubernetes-mcp-server)
	@helm uninstall mcp-server --namespace $(KCP_NS) 2>/dev/null || true
	@$(KC) delete -k manifests/mcp-server --ignore-not-found

# ── Optional add-ons ─────────────────────────────────────────────────────────

.PHONY: ai-gateway
ai-gateway: ## [optional] Deploy Envoy AI Gateway + MCPRoute in front of the front-proxy
	@$(MAKE) install-envoy
	@$(MAKE) install-mcp
	@echo ""
	@MCP_IP=$$($(KC) get svc -n envoy-gateway-system \
		-l gateway.envoyproxy.io/owning-gateway-name=kcp-mcp-gateway \
		-o jsonpath='{.items[0].status.loadBalancer.ingress[0].ip}' 2>/dev/null); \
	echo "==> ✅ AI Gateway deployed"; \
	echo "==> MCP endpoint (gateway): http://mcp.kcp.example:8080/mcp  (IP: $${MCP_IP:-pending})"; \
	echo "==> Add to /etc/hosts:      $${MCP_IP:-172.18.0.200}  mcp.kcp.example"

.PHONY: enable-oidc
enable-oidc: ## [optional] Enable local OIDC validation on access-vw (for direct callers)
	$(call info,Enabling OIDC on access-vw — config must match kcp exactly)
	@$(KC) apply -k manifests/access-vw-oidc
	$(call wait-for-deployment,$(KCP_NS),access-vw,120s)

.PHONY: disable-oidc
disable-oidc: ## Revert access-vw to requestheader-only auth
	$(call info,Disabling OIDC on access-vw)
	@$(KC) apply -k manifests/access-vw
	$(call wait-for-deployment,$(KCP_NS),access-vw,120s)

# ── Seed test data ───────────────────────────────────────────────────────────

.PHONY: seed
seed: ## Create per-user workspaces, APIBinding, RBAC
	$(call info,Seeding user workspaces and RBAC)
	@export KUBECONFIG=$(SCRIPT_DIR)/admin.kubeconfig; \
	kubectl apply -f $(REPO_ROOT)/config/apiexport/; \
	for user in alice bob; do \
		ws="$${user}-workspace"; \
		kubectl ws create "$${ws}" --type universal --enter 2>/dev/null || kubectl ws use "root:$${ws}"; \
		kubectl apply -f $(REPO_ROOT)/config/examples/apibinding-consumer.yaml; \
		kubectl apply -f $(REPO_ROOT)/config/rbac/seed-rbac-$${user}.yaml; \
		kubectl ws use ':root'; \
	done
	$(call info,Verifying SCAR)
	@sleep 5
	@TOKEN=$$(scripts/get-oidc-token.sh alice 2>/dev/null); \
	if [ -n "$$TOKEN" ]; then \
		echo "==> Testing SCAR with alice's OIDC token..."; \
		curl -sk -X POST -H "Authorization: Bearer $$TOKEN" \
		-H "Content-Type: application/json" -d "{}" \
			"https://root.kcp.example:6443/services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews" | jq .; \
	else \
		echo "⚠️  Could not get OIDC token — skipping SCAR verification"; \
	fi

# ── Full setup ───────────────────────────────────────────────────────────────

.PHONY: setup
setup: ## Full end-to-end setup (front-proxy topology; AI gateway is opt-in)
	@$(MAKE) kind-create
	@$(MAKE) install-metallb
	@$(MAKE) install-cert-manager
	@$(MAKE) install-keycloak
	@$(MAKE) install-kcp
	@$(MAKE) install-access-vw
	@$(MAKE) seed
	@echo ""
	@echo "==> ✅ Setup complete!"
	@echo ""
	@echo "==> MCP endpoint:   https://root.kcp.example:6443/services/mcp  (via front-proxy)"; \
	echo "==> SCAR endpoint:  https://root.kcp.example:6443/services/access/..."; \
	echo "==> Keycloak:       https://keycloak.kcp.example:8443  (admin/admin)"; \
	echo "==> kcp FrontProxy: https://root.kcp.example:6443"; \
	echo "==> kcp admin:      export KUBECONFIG=$(SCRIPT_DIR)/admin.kubeconfig"; \
	echo ""; \
	echo "==> Add to /etc/hosts:"; \
	echo "==>   127.0.0.1  keycloak.kcp.example root.kcp.example"; \
	echo ""; \
	echo "==> OIDC users:     alice / alice, bob / bob  (realm: kcp)"; \
	echo "==> Get token:      $(SCRIPT_DIR)/scripts/get-oidc-token.sh alice"; \
	echo ""; \
	echo "==> Optional add-ons:"; \
	echo "==>   make ai-gateway   # Envoy AI Gateway + MCPRoute (http://mcp.kcp.example:8080/mcp)"; \
	echo "==>   make enable-oidc  # local OIDC on access-vw for direct callers"

# ── Utilities ────────────────────────────────────────────────────────────────

.PHONY: get-token
get-token: ## Get OIDC token for USER (default: alice)
	@scripts/get-oidc-token.sh $(USER)

.PHONY: scar
scar: ## Test SCAR for USER (default: alice)
	@TOKEN=$$(scripts/get-oidc-token.sh $(USER) 2>/dev/null); \
	curl -sk -X POST -H "Authorization: Bearer $$TOKEN" \
		-H "Content-Type: application/json" -d "{}" \
		"https://root.kcp.example:6443/services/access/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews" | jq .
