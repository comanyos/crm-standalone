# Okta Federation Test Guide

## Setup (one-time, free Okta Developer account)

1. Sign up at https://developer.okta.com
2. Create an Application → SAML 2.0
3. Set:
   - Single sign-on URL: `http://localhost:8080/saml/{provider_id}/acs`
   - Audience URI (SP Entity ID): `http://localhost:8080/saml/{provider_id}/metadata`
4. Download the IdP metadata XML
5. Register the provider in AuthinCore:

```bash
curl -X POST http://localhost:8080/api/v1/saml/providers \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Okta Dev",
    "metadata_xml": "<paste IdP metadata here>"
  }'
```

## Test Matrix

| Test | Command | Expected |
|------|---------|----------|
| SP-initiated login | Visit `http://localhost:8080/saml/{id}/login` | Redirects to Okta, returns session |
| IdP-initiated login | Click app tile in Okta | Returns authenticated session |
| Attribute mapping | Map `email` from Okta profile | `email` in identity traits |
| Group mapping | Assign Okta group → AuthinCore role | Group claim in session |
| Logout | `GET /saml/{id}/logout` | Session destroyed, redirected to Okta SLO |
| Cert rotation | Rotate Okta signing cert | Old cert rejected after TTL |

## SCIM Provisioning (Okta → AuthinCore)

1. In Okta app → Provisioning → SCIM 2.0
2. Set SCIM connector base URL: `http://your-host/scim/v2`
3. Auth type: HTTP Header, Token: `<your SCIM token>`
4. Test connection → verify 200 from `/scim/v2/ServiceProviderConfig`
5. Enable: Push Users, Push Groups, Deactivate Users

```bash
# Issue a SCIM token for the org
curl -X POST http://localhost:8080/api/v1/orgs/{org_id}/scim-tokens \
  -H "Authorization: Bearer <admin_token>"
```

## Automated smoke test

```bash
# Set env vars from your Okta dev tenant
export OKTA_DOMAIN=dev-xxxxx.okta.com
export OKTA_CLIENT_ID=...
export OKTA_CLIENT_SECRET=...
export AUTHINCORE_URL=http://localhost:8080

go test ./test/federation/okta/... -v -timeout=120s
```
