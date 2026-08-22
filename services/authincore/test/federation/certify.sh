#!/bin/sh
set -eu

: "${AUTHINCORE_BASE_URL:?required}"
: "${TENANT_HOST:?required}"
: "${SCIM_TOKEN:?required}"

curl --fail --silent --show-error -H "Host: ${TENANT_HOST}" "${AUTHINCORE_BASE_URL}/.well-known/openid-configuration" >/dev/null
curl --fail --silent --show-error -H "Host: ${TENANT_HOST}" "${AUTHINCORE_BASE_URL}/.well-known/jwks.json" >/dev/null

user="certification-$(date +%s)@example.test"
created="$(curl --fail --silent --show-error -X POST \
  -H "Host: ${TENANT_HOST}" \
  -H "Authorization: Bearer ${SCIM_TOKEN}" \
  -H "Content-Type: application/scim+json" \
  --data "{\"schemas\":[\"urn:ietf:params:scim:schemas:core:2.0:User\"],\"userName\":\"${user}\",\"active\":true}" \
  "${AUTHINCORE_BASE_URL}/scim/v2/Users")"
printf '%s\n' "$created" | grep -q '"active":true'

printf '{"status":"partial","oidc_discovery":true,"jwks":true,"scim_create":true,"message":"Interactive vendor OIDC/SAML steps require the configured Entra/Okta browser harness."}\n'
