#!/usr/bin/env bash
# Bootstrap Zitadel for agentq: creates the OIDC device-flow client and
# the supervisor machine user with the TOKEN_EXCHANGER role.
#
# Run once after "docker compose up" when Zitadel is healthy.
#
# Usage:
#   ./deploy/zitadel-bootstrap.sh
#
# Outputs:
#   AGENTQ_CLIENT_ID    -- device-flow client for humans (agentq login)
#   SUPERVISOR_CLIENT_ID / SUPERVISOR_CLIENT_SECRET -- for agentq supervisor serve --client-id/--client-secret
#
# Prerequisites: curl, jq

set -euo pipefail

ZITADEL_URL="${ZITADEL_URL:-http://localhost:8080}"
ADMIN_USER="${ZITADEL_ADMIN_USER:-admin}"
ADMIN_PASS="${ZITADEL_ADMIN_PASS:-Password1!}"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

die() { echo "ERROR: $*" >&2; exit 1; }
need() { command -v "$1" &>/dev/null || die "$1 is required"; }
need curl
need jq

zapi() {
  local method="$1" path="$2"; shift 2
  curl -sf -X "$method" \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json" \
    "${ZITADEL_URL}${path}" "$@"
}

# ---------------------------------------------------------------------------
# 1. Obtain an admin access token via password grant
# ---------------------------------------------------------------------------

echo "==> Authenticating as admin..."

# Zitadel's personal access token endpoint for initial bootstrap
TOKEN_RESP=$(curl -sf -X POST "${ZITADEL_URL}/oauth/v2/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "grant_type=password&username=${ADMIN_USER}&password=${ADMIN_PASS}&scope=openid+urn:zitadel:iam:org:project:id:zitadel:aud" \
  2>/dev/null) || true

# Fall back: Zitadel may require a machine-user PAT for management API.
# If password grant fails, guide the user.
if [ -z "$TOKEN_RESP" ] || [ "$(echo "$TOKEN_RESP" | jq -r '.access_token // empty')" = "" ]; then
  cat <<'MSG'

  Password grant failed. Zitadel disables it by default.
  To bootstrap manually:
    1. Log in to http://localhost:8080 as admin / Password1!
    2. Create a "Personal Access Token" for the admin user under
       Users > admin > Personal Access Tokens
    3. Re-run:  ZITADEL_PAT=<token> ./deploy/zitadel-bootstrap.sh

MSG
  if [ -z "${ZITADEL_PAT:-}" ]; then
    die "Set ZITADEL_PAT to continue"
  fi
  ACCESS_TOKEN="$ZITADEL_PAT"
else
  ACCESS_TOKEN=$(echo "$TOKEN_RESP" | jq -r '.access_token')
fi

echo "    access token obtained"

# ---------------------------------------------------------------------------
# 2. Resolve the default organisation ID
# ---------------------------------------------------------------------------

echo "==> Resolving default organisation..."
ORG_ID=$(zapi GET "/management/v1/orgs/me" | jq -r '.org.id')
echo "    org id: $ORG_ID"

# ---------------------------------------------------------------------------
# 3. Create a native/device-flow OIDC application for human CLI login
# ---------------------------------------------------------------------------

echo "==> Creating device-flow OIDC application (agentq-cli)..."
APP_RESP=$(zapi POST "/management/v1/projects" \
  -d '{"name":"agentq"}') 2>/dev/null || true

PROJECT_ID=$(echo "$APP_RESP" | jq -r '.id // empty')
if [ -z "$PROJECT_ID" ]; then
  # Project may already exist; fetch it.
  PROJECT_ID=$(zapi GET "/management/v1/projects/_search" \
    -d '{"queries":[{"nameQuery":{"name":"agentq","method":"TEXT_QUERY_METHOD_EQUALS"}}]}' \
    | jq -r '.result[0].id')
fi
echo "    project id: $PROJECT_ID"

CLI_APP_RESP=$(zapi POST "/management/v1/projects/${PROJECT_ID}/apps/oidc" -d '{
  "name": "agentq-cli",
  "redirectUris": [
    "http://localhost:5173/callback",
    "http://localhost:8090/callback"
  ],
  "responseTypes": ["OIDC_RESPONSE_TYPE_CODE"],
  "grantTypes": [
    "OIDC_GRANT_TYPE_AUTHORIZATION_CODE",
    "OIDC_GRANT_TYPE_DEVICE_CODE",
    "OIDC_GRANT_TYPE_REFRESH_TOKEN"
  ],
  "appType": "OIDC_APP_TYPE_NATIVE",
  "authMethodType": "OIDC_AUTH_METHOD_TYPE_NONE",
  "devMode": true
}')
CLI_CLIENT_ID=$(echo "$CLI_APP_RESP" | jq -r '.clientId')
echo "    agentq-cli client id: $CLI_CLIENT_ID"

# ---------------------------------------------------------------------------
# 4. Create machine users (supervisor + API server)
# ---------------------------------------------------------------------------

create_machine_user() {
  local username="$1" description="$2"
  local resp uid
  resp=$(zapi POST "/management/v1/users/machine" \
    -d "{\"userName\":\"${username}\",\"name\":\"${username}\",\"description\":\"${description}\",\"accessTokenType\":\"ACCESS_TOKEN_TYPE_JWT\"}" \
    2>/dev/null) || true
  uid=$(echo "$resp" | jq -r '.userId // empty')
  if [ -z "$uid" ]; then
    uid=$(zapi GET "/management/v1/users/_search" \
      -d "{\"queries\":[{\"userNameQuery\":{\"userName\":\"${username}\",\"method\":\"TEXT_QUERY_METHOD_EQUALS\"}}]}" \
      | jq -r '.result[0].id')
  fi
  echo "$uid"
}

echo "==> Creating supervisor machine user..."
SUPERVISOR_USER_ID=$(create_machine_user "agentq-supervisor" "Service account for agentq supervisor token exchange")
echo "    supervisor user id: $SUPERVISOR_USER_ID"

echo "==> Creating API server machine user..."
API_USER_ID=$(create_machine_user "agentq-api" "Service account for agentq HTTP API server")
echo "    api user id: $API_USER_ID"

# ---------------------------------------------------------------------------
# 5. Create client credentials for each machine user
# ---------------------------------------------------------------------------

create_api_app() {
  local appname="$1"
  zapi POST "/management/v1/projects/${PROJECT_ID}/apps/api" \
    -d "{\"name\":\"${appname}\",\"authMethodType\":\"API_AUTH_METHOD_TYPE_BASIC\"}"
}

echo "==> Generating supervisor client credentials..."
SUP_CREDS=$(create_api_app "agentq-supervisor")
SUPERVISOR_CLIENT_ID=$(echo "$SUP_CREDS" | jq -r '.clientId')
SUPERVISOR_CLIENT_SECRET=$(echo "$SUP_CREDS" | jq -r '.clientSecret')

echo "==> Generating API server client credentials..."
API_CREDS=$(create_api_app "agentq-api")
API_CLIENT_ID=$(echo "$API_CREDS" | jq -r '.clientId')
API_CLIENT_SECRET=$(echo "$API_CREDS" | jq -r '.clientSecret')

# ---------------------------------------------------------------------------
# 6. Grant the supervisor the USER_IMPERSONATOR role for token exchange
#    (Zitadel action / role assignment; requires IAM org-level permissions)
# ---------------------------------------------------------------------------

echo "==> Granting token exchange role to supervisor machine user..."
zapi POST "/admin/v1/members" -d "{
  \"userId\": \"${SUPERVISOR_USER_ID}\",
  \"roles\": [\"ORG_USER_SELF_IMPERSONATION_POLICY_WRITER\"]
}" >/dev/null 2>&1 || echo "    (role grant may need manual setup -- see docs)"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

cat <<SUMMARY

==> Bootstrap complete. Add these to your .env file:

# Human CLI login
AGENTQ_CLIENT_ID=${CLI_CLIENT_ID}

# Supervisor token exchange
SUPERVISOR_CLIENT_ID=${SUPERVISOR_CLIENT_ID}
SUPERVISOR_CLIENT_SECRET=${SUPERVISOR_CLIENT_SECRET}

# entroq queue credentials (obtain access tokens for each machine user via client_credentials grant)
# POST ${ZITADEL_URL}/oauth/v2/token with client_id/client_secret to get a bearer token,
# then set each *_EQ_TOKEN to that bearer token.
# The sub claim in each token must match the corresponding entry in config/entroq-policy/data.json.
#
# Supervisor sub: ${SUPERVISOR_USER_ID}
# API server sub: ${API_USER_ID}
#
# Replace REPLACE_WITH_*_SUB in config/entroq-policy/data.json with those values.

API server JWT validation:
  --jwks-url  ${ZITADEL_URL}/oauth/v2/keys
  --issuer    ${ZITADEL_URL}

SUMMARY
