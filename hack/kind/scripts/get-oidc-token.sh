#!/usr/bin/env bash
# Get an OIDC access token from Keycloak for a given user.
# Usage: get-oidc-token.sh [username] [password]
#   Defaults: alice / alice
#
# Keycloak must be reachable at https://keycloak.kcp.example:8443
# (exposed via Kind NodePort — add to /etc/hosts: 127.0.0.1 keycloak.kcp.example)
set -euo pipefail

REALM="${REALM:-kcp}"
USERNAME="${1:-alice}"
PASSWORD="${2:-${USERNAME}}"

TOKEN=$(curl -sf --insecure -X POST "https://keycloak.kcp.example:8443/realms/${REALM}/protocol/openid-connect/token" \
  -d "client_id=kcp" \
  -d "username=${USERNAME}" \
  -d "password=${PASSWORD}" \
  -d "grant_type=password" \
  -d "scope=openid" | jq -r '.access_token')

if [ -z "${TOKEN}" ] || [ "${TOKEN}" = "null" ]; then
  echo "ERROR: Failed to get token for ${USERNAME}" >&2
  exit 1
fi

echo "${TOKEN}"
