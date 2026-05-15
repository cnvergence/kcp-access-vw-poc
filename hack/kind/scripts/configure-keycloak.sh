#!/usr/bin/env bash
# Configures Keycloak for MCP OAuth:
#   1. Creates "welcome" realm
#   2. Creates "mcp-gateway" OIDC client
#   3. Creates "mcp-access" client scope with audience mapper
#   4. Configures dynamic client registration
#
# Based on platform-mesh/local-setup/scripts/setup-mcp.sh
set -euo pipefail

KEYCLOAK_NS="${KEYCLOAK_NS:-keycloak}"
KEYCLOAK_ADMIN_USER="${KEYCLOAK_ADMIN_USER:-admin}"
KEYCLOAK_ADMIN_PASSWORD="${KEYCLOAK_ADMIN_PASSWORD:-admin}"
REALM="${REALM:-welcome}"

info() { echo "  [keycloak] $*"; }

# Port-forward to Keycloak
info "Setting up port-forward to Keycloak..."
kubectl port-forward -n "${KEYCLOAK_NS}" svc/keycloak 18080:80 &
PF_PID=$!
trap "kill ${PF_PID} 2>/dev/null || true" EXIT
sleep 3

KEYCLOAK_URL="http://localhost:18080"

# Get admin token
info "Getting admin token..."
ADMIN_TOKEN=$(curl -sf -X POST "${KEYCLOAK_URL}/realms/master/protocol/openid-connect/token" \
  -d "client_id=admin-cli" \
  -d "username=${KEYCLOAK_ADMIN_USER}" \
  -d "password=${KEYCLOAK_ADMIN_PASSWORD}" \
  -d "grant_type=password" | jq -r '.access_token')

if [ -z "${ADMIN_TOKEN}" ] || [ "${ADMIN_TOKEN}" = "null" ]; then
  echo "❌ Failed to get Keycloak admin token" >&2
  exit 1
fi

AUTH="Authorization: Bearer ${ADMIN_TOKEN}"

# Create realm
info "Creating realm '${REALM}'..."
curl -sf -X POST "${KEYCLOAK_URL}/admin/realms" \
  -H "${AUTH}" \
  -H "Content-Type: application/json" \
  -d "{
    \"realm\": \"${REALM}\",
    \"enabled\": true,
    \"registrationAllowed\": true
  }" || info "Realm may already exist, continuing..."

# Create OIDC client for MCP gateway
info "Creating 'mcp-gateway' OIDC client..."
curl -sf -X POST "${KEYCLOAK_URL}/admin/realms/${REALM}/clients" \
  -H "${AUTH}" \
  -H "Content-Type: application/json" \
  -d '{
    "clientId": "mcp-gateway",
    "name": "MCP Gateway",
    "enabled": true,
    "publicClient": false,
    "serviceAccountsEnabled": true,
    "directAccessGrantsEnabled": true,
    "standardFlowEnabled": true,
    "redirectUris": ["http://localhost:*/*", "https://localhost:*/*"],
    "webOrigins": ["*"],
    "protocol": "openid-connect"
  }' || info "Client may already exist, continuing..."

# Get client UUID
CLIENT_UUID=$(curl -sf "${KEYCLOAK_URL}/admin/realms/${REALM}/clients?clientId=mcp-gateway" \
  -H "${AUTH}" | jq -r '.[0].id')

if [ -z "${CLIENT_UUID}" ] || [ "${CLIENT_UUID}" = "null" ]; then
  echo "❌ Failed to get mcp-gateway client UUID" >&2
  exit 1
fi

# Get client secret
CLIENT_SECRET=$(curl -sf "${KEYCLOAK_URL}/admin/realms/${REALM}/clients/${CLIENT_UUID}/client-secret" \
  -H "${AUTH}" | jq -r '.value')

info "mcp-gateway client secret: ${CLIENT_SECRET}"

# Create mcp-access client scope
info "Creating 'mcp-access' client scope..."
curl -sf -X POST "${KEYCLOAK_URL}/admin/realms/${REALM}/client-scopes" \
  -H "${AUTH}" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "mcp-access",
    "description": "MCP access scope",
    "protocol": "openid-connect",
    "attributes": {
      "include.in.token.scope": "true"
    }
  }' || info "Scope may already exist, continuing..."

# Get scope UUID and add audience mapper
SCOPE_UUID=$(curl -sf "${KEYCLOAK_URL}/admin/realms/${REALM}/client-scopes" \
  -H "${AUTH}" | jq -r '.[] | select(.name=="mcp-access") | .id')

if [ -n "${SCOPE_UUID}" ] && [ "${SCOPE_UUID}" != "null" ]; then
  info "Adding audience mapper to mcp-access scope..."
  curl -sf -X POST "${KEYCLOAK_URL}/admin/realms/${REALM}/client-scopes/${SCOPE_UUID}/protocol-mappers/models" \
    -H "${AUTH}" \
    -H "Content-Type: application/json" \
    -d '{
      "name": "mcp-audience",
      "protocol": "openid-connect",
      "protocolMapper": "oidc-audience-mapper",
      "config": {
        "included.custom.audience": "mcp-access",
        "id.token.claim": "false",
        "access.token.claim": "true"
      }
    }' || info "Mapper may already exist"

  # Assign scope to mcp-gateway client
  info "Assigning mcp-access scope to mcp-gateway client..."
  curl -sf -X PUT "${KEYCLOAK_URL}/admin/realms/${REALM}/clients/${CLIENT_UUID}/default-client-scopes/${SCOPE_UUID}" \
    -H "${AUTH}" || info "Scope assignment may already exist"
fi

# Configure dynamic client registration (for MCP clients like Claude Code)
info "Configuring dynamic client registration..."

# Enable client registration
curl -sf -X PUT "${KEYCLOAK_URL}/admin/realms/${REALM}" \
  -H "${AUTH}" \
  -H "Content-Type: application/json" \
  -d "{
    \"realm\": \"${REALM}\",
    \"clientRegistrationAllowed\": true
  }" || true

# Store client secret as a Kubernetes secret for reference
kubectl create secret generic mcp-gateway-keycloak \
  --namespace "${KEYCLOAK_NS}" \
  --from-literal=client-id=mcp-gateway \
  --from-literal=client-secret="${CLIENT_SECRET}" \
  --from-literal=realm="${REALM}" \
  --dry-run=client -o yaml | kubectl apply -f -

info "Keycloak configuration complete."
info "  Realm:         ${REALM}"
info "  Client ID:     mcp-gateway"
info "  Client Secret: ${CLIENT_SECRET}"
info "  OIDC URL:      ${KEYCLOAK_URL}/realms/${REALM}/.well-known/openid-configuration"
