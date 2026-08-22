# How to Integrate Any App with AuthinCore

This guide covers every way an app can connect to AuthinCore.
Pick the pattern that fits your app.

AuthinCore is running at `http://localhost:8080` (dev). Replace with your production URL.

---

## Pattern 1 — JWT Middleware (Simplest, Recommended)

**Best for:** Go backends, any REST API

Your app validates AuthinCore JWTs locally. No round-trip to AuthinCore on every request.

### Step 1 — Register your app as an OAuth2 client

```bash
curl -X POST http://localhost:8080/api/v1/apps \
  -H "Authorization: Bearer <admin_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Vymo API",
    "type": "confidential",
    "redirect_uris": ["https://vymo.com/callback"],
    "grant_types": ["authorization_code", "refresh_token"]
  }'
# Returns: client_id, client_secret
```

### Step 2 — Copy the auth middleware

The reference implementation lives in the Vymo demo app:

```bash
cp /path/to/vymo/internal/auth/middleware.go yourapp/internal/auth/middleware.go
# Change the package import path, done.
```

### Step 3 — Wire it in your app

```go
ac := auth.New("http://localhost:8080")   // AuthinCore URL

mux.Handle("/api/me",      ac.RequireAuth(handler.Me))
mux.Handle("/api/admin",   ac.RequireRole("admin")(handler.Admin))
mux.Handle("/api/secure",  ac.RequireMFA(handler.Secure))
```

### Step 4 — User gets a token

User logs in at AuthinCore → gets JWT → calls your API with:
```
Authorization: Bearer eyJhbGci...
```

Your middleware validates the JWT against AuthinCore's JWKS, extracts identity — done. **No DB call.**

### What you get in every request

```go
claims := auth.FromContext(r.Context())
claims.Subject   // user's identity ID (UUID)
claims.OrgID     // which org they belong to
claims.Email     // their email
claims.Roles     // ["admin", "member"]
claims.AAL       // 1 = password only, 2 = MFA verified
```

---

## Pattern 2 — OIDC Authorization Code Flow (Web App Login)

**Best for:** Web apps where users log in via a browser

```
User clicks "Login" → redirect to AuthinCore → user logs in →
AuthinCore redirects back with code → your app exchanges code for token
```

### Flow

```
1. Your app redirects user to:
   GET http://localhost:8080/oauth2/authorize
     ?client_id=YOUR_CLIENT_ID
     &redirect_uri=https://yourapp.com/callback
     &response_type=code
     &scope=openid email profile
     &state=random_string
     &code_challenge=S256_hash     ← PKCE (prevents token theft)
     &code_challenge_method=S256

2. User logs in at AuthinCore (password, MFA, passkey — all handled)

3. AuthinCore redirects back:
   GET https://yourapp.com/callback?code=abc123&state=random_string

4. Your app exchanges the code:
   POST http://localhost:8080/oauth2/token
     grant_type=authorization_code
     &code=abc123
     &client_id=YOUR_CLIENT_ID
     &client_secret=YOUR_SECRET
     &redirect_uri=https://yourapp.com/callback
     &code_verifier=original_random   ← PKCE verifier

5. You get back:
   {
     "access_token": "eyJ...",
     "refresh_token": "...",
     "id_token": "eyJ...",
     "expires_in": 900
   }
```

---

## Pattern 3 — Machine to Machine (No User)

**Best for:** Background jobs, microservices, CI/CD

```bash
curl -X POST http://localhost:8080/oauth2/token \
  -d "grant_type=client_credentials" \
  -d "client_id=YOUR_SERVICE_CLIENT" \
  -d "client_secret=YOUR_SECRET" \
  -d "scope=internal:read internal:write"

# Returns access token — use for service-to-service calls
```

---

## Pattern 4 — Fine-Grained Authorization (FGA)

**Best for:** Per-resource permissions (not just roles)

Use this when role-based access is not granular enough. Example: user is a `manager` (role) but can only edit *their own* leads, not all leads.

### Grant a permission

```bash
# Alice can edit lead:123 in org abc-uuid
curl -X POST http://localhost:8080/authz/tuples \
  -H "Content-Type: application/json" \
  -d '{
    "org_id": "abc-uuid",
    "object_type": "lead",
    "object_id": "lead:123",
    "relation": "editor",
    "subject_type": "user",
    "subject_id": "alice-uuid"
  }'
```

### Check a permission

```bash
curl -X POST http://localhost:8080/authz/check \
  -H "Content-Type: application/json" \
  -d '{
    "org_id": "abc-uuid",
    "object_type": "lead",
    "object_id": "lead:123",
    "relation": "viewer",
    "subject_type": "user",
    "subject_id": "alice-uuid"
  }'
# {"allowed": true}  ← alice is editor, editor implies viewer
```

### In your app (Go example)

```go
allowed, _ := fgaClient.Check(ctx, CheckRequest{
    OrgID: orgID, ObjectType: "lead", ObjectID: leadID,
    Relation: "editor", SubjectType: "user", SubjectID: userID,
})
if !allowed {
    http.Error(w, "forbidden", http.StatusForbidden)
    return
}
```

---

## Pattern 5 — SAML SSO (Enterprise customers)

**Best for:** B2B apps where enterprise customers use Okta / Entra ID

### Give the customer your SP metadata URL

```
http://localhost:8080/saml/{provider_id}/metadata
```

### Register their IdP in AuthinCore

```bash
curl -X POST http://localhost:8080/api/v1/saml/providers \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Acme Corp Okta",
    "metadata_url": "https://acme.okta.com/app/xxx/sso/saml/metadata"
  }'
```

### User login

User visits `http://localhost:8080/saml/{provider_id}/login` → redirected to Okta → comes back authenticated.
Your app gets a normal JWT — no SAML code in your app.

---

## Pattern 6 — SCIM Provisioning (Auto sync users from Okta/Entra)

**Best for:** Enterprise customers who want zero manual user management

### Issue a SCIM token for the org

```bash
curl -X POST http://localhost:8080/api/v1/orgs/{org_id}/scim-tokens \
  -H "Authorization: Bearer <admin_token>"
# Returns: scim_token
```

### Give the customer these details

```
SCIM Base URL:  http://localhost:8080/scim/v2
Auth Type:      Bearer Token
Token:          <scim_token>
```

Customer configures in Okta/Entra → users are auto-provisioned from that point.

---

## Pattern 7 — Blueprint API (Declarative Org Config)

**Best for:** Spinning up a new org with a known config, GitOps-style configuration management

Apply a full org setup in a single call — flows, policies, webhook endpoints, actions.

### Apply a blueprint

```bash
curl -X POST http://localhost:8080/api/v1/admin/blueprints \
  -H "Authorization: Bearer <admin_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "org_id": "abc-uuid",
    "dry_run": false,
    "blueprint": {
      "version": "1",
      "flows": [
        { "type": "login", "strategies": ["password", "totp"] }
      ],
      "policies": [
        { "binding": "flow.login.post", "rules": [{ "type": "ip_reputation" }] }
      ],
      "webhooks": [
        { "url": "https://your-app.com/hooks/auth", "events": ["user.login", "user.created"] }
      ]
    }
  }'
```

### Dry run (validate without writing)

```bash
# Add "dry_run": true — returns what would change, writes nothing
-d '{ "org_id": "abc-uuid", "dry_run": true, "blueprint": { ... } }'
```

### List applied blueprints

```bash
curl "http://localhost:8080/api/v1/admin/blueprints?org_id=abc-uuid" \
  -H "Authorization: Bearer <admin_token>"
# Returns: [{"id": "...", "version": "1", "applied_at": "..."}]
```

---

## Pattern 8 — Webhooks (Receive Auth Events)

**Best for:** Triggering downstream actions when users log in, register, or are deactivated

### Register a webhook for your app

```bash
curl -X POST http://localhost:8080/api/v1/orgs/{org_id}/webhooks \
  -H "Authorization: Bearer <admin_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://your-app.com/hooks/auth",
    "events": ["user.login", "user.created", "session.revoked"],
    "signing_secret": "your-secret-here"
  }'
```

### Verify incoming webhook in your app (Go)

```go
func verifyWebhook(r *http.Request, body []byte, secret string) bool {
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write(body)
    expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
    got := r.Header.Get("X-Authincore-Signature")
    return hmac.Equal([]byte(got), []byte(expected))
}
```

### Payload shape

```json
{
  "id": "uuid",
  "type": "user.login",
  "createTime": "2026-06-10T08:00:00Z",
  "orgId": "abc-uuid",
  "data": {
    "event_id": "...",
    "aggregate_type": "identity",
    "aggregate_id": "user-uuid",
    "event_type": "user.login"
  }
}
```

---

## Integration Checklist

For every new app connecting to AuthinCore:

- [ ] Register app as OAuth2 client → get `client_id` + `client_secret`
- [ ] Copy `auth/middleware.go` → update import path
- [ ] Set `AUTHINCORE_URL` environment variable
- [ ] Wrap protected routes with `ac.RequireAuth(...)`
- [ ] Add `ac.RequireRole(...)` for admin routes
- [ ] Add `ac.RequireMFA(...)` for sensitive actions
- [ ] Wire FGA check calls for per-resource permissions (if needed)
- [ ] Register a webhook endpoint to receive auth events (if needed)
- [ ] Apply a blueprint for declarative org config (if needed)
- [ ] Test with a real token from AuthinCore

Average integration time: **under 1 day.**

---

## SDK / Language Support

| Language | How |
|----------|-----|
| **Go** | Copy `internal/auth/middleware.go` (reference: Vymo demo app) |
| **Node.js** | Use `jose` library + `GET /oauth2/keys` (JWKS endpoint) |
| **Python** | Use `python-jose` + JWKS endpoint |
| **Any language** | Validate RS256 JWT against `GET /oauth2/keys` |

The JWKS endpoint (`/oauth2/keys`) is public — any JWT library can use it.
