#!/usr/bin/env bash
# Showcase script for the Access Virtual Workspace (SCAR API).
#
# Demonstrates that access-vw correctly enforces RBAC:
#   - authorized users/groups see their workspaces
#   - unauthorized users get empty results
#   - dynamic grant/revoke is reflected in real time
#
# Prerequisites:
#   - kcp running (kcp start)
#   - access-vw running with -trust-headers (make run-access-vw)
#   - APIExport installed, test workspace created, RBAC seeded
#     (make install-apiexport create-test-workspace seed-rbac)
#
# Usage:
#   ./hack/demo.sh               # run all scenarios
#   ./hack/demo.sh --no-dynamic  # skip grant/revoke (non-destructive)

set -euo pipefail

SCAR_URL="${SCAR_URL:-http://localhost:9099/services/access-virtual-workspace/apis/access.kcp.io/v1alpha1/selfclusteraccessreviews}"
DEBUG_URL="${DEBUG_URL:-http://localhost:9099/debug/graph}"
EXPORT_PATH="${EXPORT_PATH:-root}"
TEST_WORKSPACE="${TEST_WORKSPACE:-test-workspace}"

SKIP_DYNAMIC=false
if [[ "${1:-}" == "--no-dynamic" ]]; then
  SKIP_DYNAMIC=true
fi

# ── Helpers ──────────────────────────────────────────────────────────

GREEN='\033[0;32m'
RED='\033[0;31m'
CYAN='\033[0;36m'
YELLOW='\033[0;33m'
DIM='\033[2m'
BOLD='\033[1m'
RESET='\033[0m'

pass=0
fail=0

banner() {
  echo ""
  echo -e "${BOLD}${CYAN}══════════════════════════════════════════════════════════════${RESET}"
  echo -e "${BOLD}${CYAN}  $1${RESET}"
  echo -e "${BOLD}${CYAN}══════════════════════════════════════════════════════════════${RESET}"
}

scenario() {
  echo ""
  echo -e "${BOLD}▸ $1${RESET}"
  echo -e "${DIM}  $2${RESET}"
}

scar() {
  # Usage: scar [-H header]...
  curl -sf -X POST "$@" "${SCAR_URL}" 2>/dev/null || echo '{"error": "request failed"}'
}

assert_clusters_nonempty() {
  local label="$1"
  local response="$2"
  local count
  count=$(echo "$response" | jq -r '.status.clusters | length' 2>/dev/null || echo "0")
  if [[ "$count" -gt 0 ]]; then
    echo -e "  ${GREEN}✓ PASS${RESET} — $label (${count} workspace(s) returned)"
    ((pass++))
  else
    echo -e "  ${RED}✗ FAIL${RESET} — $label (expected clusters, got none)"
    ((fail++))
  fi
}

assert_clusters_empty() {
  local label="$1"
  local response="$2"
  local count
  count=$(echo "$response" | jq -r '.status.clusters | length' 2>/dev/null || echo "-1")
  if [[ "$count" -eq 0 ]]; then
    echo -e "  ${GREEN}✓ PASS${RESET} — $label (empty clusters, as expected)"
    ((pass++))
  else
    echo -e "  ${RED}✗ FAIL${RESET} — $label (expected empty, got ${count} cluster(s))"
    ((fail++))
  fi
}

show_response() {
  echo "$1" | jq -C '.' 2>/dev/null | sed 's/^/    /'
}

# ── Preflight ────────────────────────────────────────────────────────

banner "Access VW Demo — RBAC Enforcement Showcase"

echo ""
echo -e "${DIM}Checking access-vw is reachable...${RESET}"
if ! curl -sf http://localhost:9099/healthz >/dev/null 2>&1; then
  echo -e "${RED}✗ access-vw is not running on :9099${RESET}"
  echo "  Start it with: make run-access-vw"
  exit 1
fi
echo -e "${GREEN}✓ access-vw is healthy${RESET}"

echo ""
echo -e "${DIM}Current graph state:${RESET}"
curl -sf "${DEBUG_URL}" 2>/dev/null | jq -C '.' | sed 's/^/    /'

# ── Scenario 1: Authorized user ─────────────────────────────────────

banner "1. Authorized User — Happy Path"

scenario "User 'alice' has a direct ClusterRoleBinding" \
         "curl -X POST -H 'X-Remote-User: alice' → should return workspace(s)"

resp=$(scar -H 'X-Remote-User: alice')
assert_clusters_nonempty "alice sees her workspace" "$resp"
show_response "$resp"

# ── Scenario 2: Authorized group ────────────────────────────────────

banner "2. Authorized Group — Happy Path"

scenario "User 'someone' in group 'eng' has access via group CRB" \
         "curl -X POST -H 'X-Remote-User: someone' -H 'X-Remote-Group: eng' → should return workspace(s)"

resp=$(scar -H 'X-Remote-User: someone' -H 'X-Remote-Group: eng')
assert_clusters_nonempty "group 'eng' grants access" "$resp"
show_response "$resp"

# ── Scenario 3: Multiple groups ─────────────────────────────────────

banner "3. Multiple Groups — Union Semantics"

scenario "User 'alice' in groups 'eng' AND 'platform'" \
         "Both groups grant access to the same workspace — result is deduplicated"

resp=$(scar -H 'X-Remote-User: alice' -H 'X-Remote-Group: eng' -H 'X-Remote-Group: platform')
assert_clusters_nonempty "alice + eng + platform gets access" "$resp"
cluster_count=$(echo "$resp" | jq -r '.status.clusters | length')
echo -e "  ${DIM}(returned ${cluster_count} workspace(s) — overlapping grants are deduplicated)${RESET}"
show_response "$resp"

# ── Scenario 4: Unauthorized user ───────────────────────────────────

banner "4. Unauthorized User — No Access"

scenario "User 'bob' has no CRB anywhere" \
         "curl -X POST -H 'X-Remote-User: bob' → should return empty clusters"

resp=$(scar -H 'X-Remote-User: bob')
assert_clusters_empty "bob sees nothing" "$resp"
show_response "$resp"

# ── Scenario 5: Unauthorized group ──────────────────────────────────

banner "5. Unauthorized Group — No Access"

scenario "User 'someone' in group 'finance' (no CRB for this group)" \
         "curl -X POST -H 'X-Remote-User: someone' -H 'X-Remote-Group: finance' → empty"

resp=$(scar -H 'X-Remote-User: someone' -H 'X-Remote-Group: finance')
assert_clusters_empty "group 'finance' has no access" "$resp"
show_response "$resp"

# ── Scenario 6: No authentication ───────────────────────────────────

banner "6. No Identity — Missing Headers"

scenario "Request with no X-Remote-User header" \
         "Should return empty (no identity = no access)"

resp=$(scar)
assert_clusters_empty "no headers = no access" "$resp"
show_response "$resp"

# ── Scenario 7: Dynamic revocation ──────────────────────────────────

if [[ "$SKIP_DYNAMIC" == "false" ]]; then

banner "7. Dynamic Revocation — Remove Access in Real Time"

scenario "Delete alice's CRB → SCAR should stop returning her workspace" \
         "kubectl delete crb → wait 3s → query SCAR"

echo -e "  ${DIM}Deleting alice's CRB...${RESET}"
kubectl ws use "${EXPORT_PATH}/${TEST_WORKSPACE}" >/dev/null 2>&1
kubectl delete clusterrolebinding access-vw-test--alice-viewer --ignore-not-found >/dev/null 2>&1
kubectl ws use "${EXPORT_PATH}" >/dev/null 2>&1

echo -e "  ${DIM}Waiting 3s for graph update...${RESET}"
sleep 3

resp=$(scar -H 'X-Remote-User: alice')
assert_clusters_empty "alice lost access after CRB deletion" "$resp"
show_response "$resp"

# ── Scenario 8: Dynamic grant ───────────────────────────────────────

banner "8. Dynamic Grant — Restore Access in Real Time"

scenario "Re-apply alice's CRB → SCAR should return her workspace again" \
         "kubectl apply → wait 3s → query SCAR"

echo -e "  ${DIM}Re-creating alice's CRB...${RESET}"
kubectl ws use "${EXPORT_PATH}/${TEST_WORKSPACE}" >/dev/null 2>&1
kubectl apply -f - >/dev/null 2>&1 <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: access-vw-test--alice-viewer
subjects:
  - kind: User
    name: alice
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: view
  apiGroup: rbac.authorization.k8s.io
EOF
kubectl ws use "${EXPORT_PATH}" >/dev/null 2>&1

echo -e "  ${DIM}Waiting 3s for graph update...${RESET}"
sleep 3

resp=$(scar -H 'X-Remote-User: alice')
assert_clusters_nonempty "alice regained access after CRB re-creation" "$resp"
show_response "$resp"

else
  echo ""
  echo -e "${YELLOW}⊘ Skipping dynamic grant/revoke scenarios (--no-dynamic)${RESET}"
fi

# ── Summary ──────────────────────────────────────────────────────────

banner "Results"

total=$((pass + fail))
echo ""
if [[ "$fail" -eq 0 ]]; then
  echo -e "  ${GREEN}${BOLD}All ${total} checks passed ✓${RESET}"
else
  echo -e "  ${GREEN}${pass} passed${RESET}, ${RED}${fail} failed${RESET} out of ${total}"
fi
echo ""

exit "$fail"
