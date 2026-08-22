# MVP Features — Core Login → Token Flow

Target: a user can register, log in with password, receive a JWT, and refresh it.  
Estimated effort: **3–4 weeks** after critical blockers are resolved.

---

## Phase 1 — Registration + Login (Week 1)

### 1.1 Registration Flow
- [ ] `POST /self-service/registration` creates an identity + password credential in one transaction
- [ ] Validate traits against the org's JSON Schema (wire `identity/schema/validator.go`)
- [ ] Check for duplicate email before insert
- [ ] On success: create session → return session token

### 1.2 Login Flow
- [ ] `POST /self-service/login` resolves identifier → fetches password hash → argon2id verify
- [ ] On success: create session via `command.CreateSession` → mint access token + refresh token
- [ ] Return `{ access_token, refresh_token, expires_in }` in response

### 1.3 Session Cookie Support
- [ ] Set `ory_session` cookie on login response (same pattern as Kratos)
- [ ] Read session from cookie in `authn` interceptor
- [ ] `GET /api/v1/sessions/me` (WhoAmI) returns identity + session from cookie/bearer

---

## Phase 2 — OAuth2 / OIDC End-to-End (Week 2)

### 2.1 Authorization Code + PKCE
- [ ] `GET /oauth2/authorize` → creates login flow, redirects browser
- [ ] After login, call `oidc.IssueAuthCode` and redirect to client `redirect_uri?code=...`
- [ ] `POST /oauth2/token` (auth_code grant) — exchange code for access + refresh + ID token

### 2.2 Token Refresh
- [ ] `POST /oauth2/token` (refresh_token grant) — consume refresh token, issue new pair
- [ ] Refresh token rotation: old token invalidated on use

### 2.3 ID Token
- [ ] Mint ID token (JWT) with `sub`, `email`, `name`, `iss`, `aud`, `iat`, `exp`
- [ ] Populate claims from identity traits using a simple dot-path mapper

### 2.4 JWKS
- [ ] `GET /oauth2/keys` returns the RS256 public key — already implemented, verify with a real client

---

## Phase 3 — Recovery + Verification (Week 3)

### 3.1 Recovery Flow
- [ ] Generate recovery code → store in DB with expiry
- [ ] Enqueue courier message → `courier.Send(ctx, orgID, TypeRecoveryCode, email, data)`
- [ ] Submit recovery code → verify → allow password reset → invalidate code

### 3.2 Email Verification
- [ ] On registration, enqueue verification email
- [ ] `POST /self-service/verification` with code → mark identity email as verified

### 3.3 SMTP Sender
- [ ] Implement `courier.Sender` backed by SMTP (`net/smtp` or `github.com/wneessen/go-mail`)
- [ ] Template: recovery, verification, login code
- [ ] Wire into `courier.New(queue, smtpSender, tplLoader, log)`

---

## Phase 4 — Settings + Token Revocation (Week 4)

### 4.1 Settings Flow
- [ ] Change password (verify current → hash new → update credential)
- [ ] Update traits (name, avatar URL, etc.)

### 4.2 Session Management
- [ ] `DELETE /api/v1/sessions/{id}` — revoke a single session (already wired, needs repo)
- [ ] `DELETE /api/v1/sessions` — revoke all sessions for identity
- [ ] Revoked sessions removed from Redis cache immediately

### 4.3 Token Revocation
- [ ] `POST /oauth2/revoke` — invalidate a refresh token
- [ ] Blacklist access tokens in Redis by `jti` until natural expiry

---

## Status Tracker

**Phase 1**
- [ ] Registration creates identity + credential
- [ ] Login verifies password + creates session + mints tokens
- [ ] WhoAmI returns identity from session cookie/bearer

**Phase 2**
- [ ] Authorization code flow end-to-end
- [ ] Token refresh with rotation
- [ ] ID token minted with correct claims
- [ ] JWKS verified with a real OIDC client (e.g. Postman)

**Phase 3**
- [ ] Recovery code sent via email
- [ ] Recovery code verified + password reset
- [ ] Email verification on registration

**Phase 4**
- [ ] Password change via settings flow
- [ ] Session revocation + cache invalidation
- [ ] Refresh token revocation endpoint
