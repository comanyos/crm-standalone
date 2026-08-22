# Microsoft Entra ID Federation Test Guide

## Setup (free Azure subscription)

1. Azure Portal → Entra ID → Enterprise Applications → New Application → Create your own
2. Select "Integrate any other application you don't find in the gallery"
3. Set up Single Sign-On → SAML
4. Basic SAML Configuration:
   - Identifier (Entity ID): `http://localhost:8080/saml/{provider_id}/metadata`
   - Reply URL (ACS): `http://localhost:8080/saml/{provider_id}/acs`
5. Download Federation Metadata XML
6. Register in AuthinCore:

```bash
curl -X POST http://localhost:8080/api/v1/saml/providers \
  -H "Content-Type: application/json" \
  -d '{"name": "Entra ID", "metadata_xml": "<paste xml>"}'
```

## Test Matrix

| Test | Steps | Expected |
|------|-------|----------|
| SP-initiated | Visit `/saml/{id}/login` | Redirects to Microsoft login, returns session |
| MFA passthrough | Enable Conditional Access MFA in Entra | MFA enforced before assertion |
| Group claim | Add group claim in Entra token config | `groups` in SAML attributes |
| UPN attribute | Map `userprincipalname` to email trait | Email populated from Entra |
| Guest users | Invite B2B guest, attempt login | Guest assertion accepted |
| Token lifetime | Set short token lifetime in Entra | Re-auth required correctly |

## SCIM Provisioning (Entra → AuthinCore)

1. Entra ID → Enterprise App → Provisioning → Automatic
2. Tenant URL: `https://your-host/scim/v2`
3. Secret Token: `<your SCIM token>`
4. Test Connection → Save
5. Mappings → Synchronize Entra Users:
   - `userPrincipalName` → `userName`
   - `mail` → `emails[type eq "work"].value`
   - `accountEnabled` → `active`

## Known Entra SAML quirks to test

- Entra sends `NameID` as UPN, not email — verify attribute mapping
- Entra signs with SHA-256; verify AuthinCore rejects SHA-1
- Entra clock skew can be up to 5 minutes — verify NotBefore tolerance
- Large group membership sends groups as OIDs, not names — verify handling

## Run

```bash
export ENTRA_TENANT_ID=...
export ENTRA_METADATA_URL=https://login.microsoftonline.com/{tenant}/federationmetadata/2007-06/federationmetadata.xml
export AUTHINCORE_URL=http://localhost:8080

go test -tags=federation ./test/federation/entra/... -v
```
