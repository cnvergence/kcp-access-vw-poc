#!/usr/bin/env bash
# Configures Keycloak for kcp OIDC + MCP OAuth:
#   1. Creates "kcp" realm
#   2. Creates "kcp" OIDC client (for kcp apiserver OIDC validation)
#   3. Creates "mcp-gateway" OIDC client (for MCP OAuth)
#   4. Creates test users alice and bob
#   5. Configures dynamic client registration (for MCP clients like Claude Code)
#
# Based on platform-mesh/local-setup/scripts/setup-mcp.sh
set -euo pipefail

KEYCLOAK_NS="${KEYCLOAK_NS:-keycloak}"
KEYCLOAK_ADMIN_USER="${KEYCLOAK_ADMIN_USER:-admin}"
KEYCLOAK_ADMIN_PASSWORD="${KEYCLOAK_ADMIN_PASSWORD:-admin}"
REALM="${REALM:-kcp}"

info() { echo "  [keycloak] $*"; }

# Keycloak is exposed via Kind NodePort at keycloak.kcp.example:8443
KEYCLOAK_URL="https://keycloak.kcp.example:8443"
CURL="curl -sf --insecure"

# Get admin token
info "Getting admin token..."
ADMIN_TOKEN=$(${CURL} -X POST "${KEYCLOAK_URL}/realms/master/protocol/openid-connect/token" \
  -d "client_id=admin-cli" \
  -d "username=${KEYCLOAK_ADMIN_USER}" \
  -d "password=${KEYCLOAK_ADMIN_PASSWORD}" \
  -d "grant_type=password" | jq -r '.access_token')

if [ -z "${ADMIN_TOKEN}" ] || [ "${ADMIN_TOKEN}" = "null" ]; then
  echo "❌ Failed to get Keycloak admin token" >&2
  exit 1
fi

AUTH="Authorization: Bearer ${ADMIN_TOKEN}"

# ── Realm ──────────────────────────────────────────────────────────────────

info "Creating realm '${REALM}'..."
${CURL} -X POST "${KEYCLOAK_URL}/admin/realms" \
  -H "${AUTH}" \
  -H "Content-Type: application/json" \
  -d "{
    \"realm\": \"${REALM}\",
    \"enabled\": true,
    \"registrationAllowed\": true
  }" || info "Realm may already exist, continuing..."

# ── kcp OIDC client (for kcp apiserver token validation) ───────────────────

info "Creating 'kcp' OIDC client..."
${CURL} -X POST "${KEYCLOAK_URL}/admin/realms/${REALM}/clients" \
  -H "${AUTH}" \
  -H "Content-Type: application/json" \
  -d '{
    "clientId": "kcp",
    "name": "kcp API Server",
    "enabled": true,
    "publicClient": true,
    "directAccessGrantsEnabled": true,
    "standardFlowEnabled": true,
    "redirectUris": ["*"],
    "webOrigins": ["*"],
    "protocol": "openid-connect"
  }' || info "Client may already exist, continuing..."

# Add audience mapper so tokens include "kcp" in the aud claim
KCP_CLIENT_UUID=$(${CURL} "${KEYCLOAK_URL}/admin/realms/${REALM}/clients?clientId=kcp" \
  -H "${AUTH}" | jq -r '.[0].id')

${CURL} -X POST "${KEYCLOAK_URL}/admin/realms/${REALM}/clients/${KCP_CLIENT_UUID}/protocol-mappers/models" \
  -H "${AUTH}" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "kcp-audience",
    "protocol": "openid-connect",
    "protocolMapper": "oidc-audience-mapper",
    "config": {
      "included.client.audience": "kcp",
      "id.token.claim": "true",
      "access.token.claim": "true"
    }
  }' || info "Audience mapper may already exist"

# ── MCP Gateway OIDC client ───────────────────────────────────────────────

info "Creating 'mcp-gateway' OIDC client..."
${CURL} -X POST "${KEYCLOAK_URL}/admin/realms/${REALM}/clients" \
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
    "redirectUris": ["http://localhost:*/*", "https://localhost:*/*", "http://mcp.kcp.example:*/*"],
    "webOrigins": ["*"],
    "protocol": "openid-connect"
  }' || info "Client may already exist, continuing..."

# Get mcp-gateway client UUID and secret
MCP_CLIENT_UUID=$(${CURL} "${KEYCLOAK_URL}/admin/realms/${REALM}/clients?clientId=mcp-gateway" \
  -H "${AUTH}" | jq -r '.[0].id')

MCP_CLIENT_SECRET=$(${CURL} "${KEYCLOAK_URL}/admin/realms/${REALM}/clients/${MCP_CLIENT_UUID}/client-secret" \
  -H "${AUTH}" | jq -r '.value')

info "mcp-gateway client secret: ${MCP_CLIENT_SECRET}"

# ── Client scope with audience mapper ──────────────────────────────────────

info "Creating 'mcp-access' client scope..."
${CURL} -X POST "${KEYCLOAK_URL}/admin/realms/${REALM}/client-scopes" \
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

SCOPE_UUID=$(${CURL} "${KEYCLOAK_URL}/admin/realms/${REALM}/client-scopes" \
  -H "${AUTH}" | jq -r '.[] | select(.name=="mcp-access") | .id')

if [ -n "${SCOPE_UUID}" ] && [ "${SCOPE_UUID}" != "null" ]; then
  info "Adding audience mappers to mcp-access scope..."
  ${CURL} -X POST "${KEYCLOAK_URL}/admin/realms/${REALM}/client-scopes/${SCOPE_UUID}/protocol-mappers/models" \
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

  # Add kcp audience so tokens from dynamically registered clients
  # pass kcp OIDC validation (--oidc-client-id=kcp requires aud=kcp)
  ${CURL} -X POST "${KEYCLOAK_URL}/admin/realms/${REALM}/client-scopes/${SCOPE_UUID}/protocol-mappers/models" \
    -H "${AUTH}" \
    -H "Content-Type: application/json" \
    -d '{
      "name": "kcp-audience",
      "protocol": "openid-connect",
      "protocolMapper": "oidc-audience-mapper",
      "config": {
        "included.client.audience": "kcp",
        "id.token.claim": "true",
        "access.token.claim": "true"
      }
    }' || info "kcp audience mapper may already exist"

  ${CURL} -X PUT "${KEYCLOAK_URL}/admin/realms/${REALM}/clients/${MCP_CLIENT_UUID}/default-client-scopes/${SCOPE_UUID}" \
    -H "${AUTH}" || info "Scope assignment may already exist"

  # Make mcp-access a realm default scope so dynamically registered clients get it
  ${CURL} -X PUT "${KEYCLOAK_URL}/admin/realms/${REALM}/default-default-client-scopes/${SCOPE_UUID}" \
    -H "${AUTH}" || info "Realm default scope may already exist"
fi

# ── Test users ─────────────────────────────────────────────────────────────

create_user() {
  local username="$1" password="$2"
  info "Creating user '${username}'..."
  ${CURL} -X POST "${KEYCLOAK_URL}/admin/realms/${REALM}/users" \
    -H "${AUTH}" \
    -H "Content-Type: application/json" \
    -d "{
      \"username\": \"${username}\",
      \"enabled\": true,
      \"emailVerified\": true,
      \"email\": \"${username}@kcp.example\",
      \"firstName\": \"${username}\",
      \"lastName\": \"User\",
      \"requiredActions\": [],
      \"credentials\": [{
        \"type\": \"password\",
        \"value\": \"${password}\",
        \"temporary\": false
      }]
    }" || info "User '${username}' may already exist, continuing..."
}

create_user "alice" "alice"
create_user "bob" "bob"

# ── Dynamic client registration ───────────────────────────────────────────

info "Configuring dynamic client registration..."
${CURL} -X PUT "${KEYCLOAK_URL}/admin/realms/${REALM}" \
  -H "${AUTH}" \
  -H "Content-Type: application/json" \
  -d "{
    \"realm\": \"${REALM}\",
    \"clientRegistrationAllowed\": true
  }" || true

# Update Trusted Hosts policy for anonymous registration
TRUSTED_HOSTS_COMPONENT=$(${CURL} "${KEYCLOAK_URL}/admin/realms/${REALM}/components?type=org.keycloak.services.clientregistration.policy.ClientRegistrationPolicy" \
  -H "${AUTH}" | jq -r '[.[] | select(.name == "Trusted Hosts" and .subType == "anonymous")] | .[0]')
TRUSTED_HOSTS_ID=$(echo "$TRUSTED_HOSTS_COMPONENT" | jq -r '.id // empty')
PARENT_ID=$(echo "$TRUSTED_HOSTS_COMPONENT" | jq -r '.parentId // empty')

if [ -n "$TRUSTED_HOSTS_ID" ]; then
  info "Updating Trusted Hosts policy..."
  ${CURL} -X PUT "${KEYCLOAK_URL}/admin/realms/${REALM}/components/${TRUSTED_HOSTS_ID}" \
    -H "${AUTH}" \
    -H "Content-Type: application/json" \
    -d "{
      \"id\": \"${TRUSTED_HOSTS_ID}\",
      \"name\": \"Trusted Hosts\",
      \"providerId\": \"trusted-hosts\",
      \"providerType\": \"org.keycloak.services.clientregistration.policy.ClientRegistrationPolicy\",
      \"parentId\": \"${PARENT_ID}\",
      \"subType\": \"anonymous\",
      \"config\": {
        \"host-sending-registration-request-must-match\": [\"false\"],
        \"client-uris-must-match\": [\"true\"],
        \"trusted-hosts\": [\"localhost\", \"mcp.kcp.example\", \"127.0.0.1\"]
      }
    }" || true
fi

# Update Allowed Client Scopes policy for anonymous registration
SCOPES_COMPONENT=$(${CURL} "${KEYCLOAK_URL}/admin/realms/${REALM}/components?type=org.keycloak.services.clientregistration.policy.ClientRegistrationPolicy" \
  -H "${AUTH}" | jq -r '[.[] | select(.name == "Allowed Client Scopes" and .subType == "anonymous")] | .[0]')
SCOPES_ID=$(echo "$SCOPES_COMPONENT" | jq -r '.id // empty')
SCOPES_PARENT_ID=$(echo "$SCOPES_COMPONENT" | jq -r '.parentId // empty')

if [ -n "$SCOPES_ID" ]; then
  info "Updating Allowed Client Scopes policy..."
  ${CURL} -X PUT "${KEYCLOAK_URL}/admin/realms/${REALM}/components/${SCOPES_ID}" \
    -H "${AUTH}" \
    -H "Content-Type: application/json" \
    -d "{
      \"id\": \"${SCOPES_ID}\",
      \"name\": \"Allowed Client Scopes\",
      \"providerId\": \"allowed-client-templates\",
      \"providerType\": \"org.keycloak.services.clientregistration.policy.ClientRegistrationPolicy\",
      \"parentId\": \"${SCOPES_PARENT_ID}\",
      \"subType\": \"anonymous\",
      \"config\": {
        \"allow-default-scopes\": [\"true\"],
        \"allowed-client-scopes\": [\"openid\", \"profile\", \"email\", \"roles\", \"web-origins\", \"acr\", \"basic\", \"mcp-access\", \"offline_access\", \"address\", \"phone\", \"microprofile-jwt\"]
      }
    }" || true
fi

# ── Store secrets ──────────────────────────────────────────────────────────

kubectl create secret generic mcp-gateway-keycloak \
  --namespace "${KEYCLOAK_NS}" \
  --from-literal=client-id=mcp-gateway \
  --from-literal=client-secret="${MCP_CLIENT_SECRET}" \
  --from-literal=realm="${REALM}" \
  --dry-run=client -o yaml | kubectl apply -f -

info "Keycloak configuration complete."
info "  Realm:         ${REALM}"
info "  kcp client:    kcp (public)"
info "  MCP client:    mcp-gateway (secret: ${MCP_CLIENT_SECRET})"
info "  Users:         alice / alice, bob / bob"
info "  OIDC URL:      ${KEYCLOAK_URL}/realms/${REALM}/.well-known/openid-configuration"
