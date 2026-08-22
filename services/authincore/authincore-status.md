# AuthinCore — Status Overlay for `authincore.png`

Per-element readiness for the architecture infographic. The graphic shows the **target model**; this overlay shows **what's actually live** as of 2026-06-18.

**Legend:** ✅ done & working · 🟡 built but a gap blocks real use · ❌ missing / not built · 📋 business decision (not code)

> **2026-06-18 update:** A hosted login UI now exists (`hosted-ui/dist/` — login, registration, recovery, verification, branding-aware) and is now **embedded into and served by the Go binary** (`hosted-ui/embed.go` → `Deps.HostedUI`, SPA catch-all in `server.go`). The `GET /api/v1/public/branding` endpoint + enterprise store wiring (`applyEnterpriseDeps`) were added, so custom branding now reaches the login page in enterprise builds. Remaining hosted-UI gaps: no `hosted-ui/src/` checked in (only the built bundle), no MFA/social/consent screens yet, and SAML `linker`/`minter` are still `nil`. See the per-panel notes below.

---

## Panel 1 — OSS vs Enterprise

**OSS (all ✅):**
Password ✅ · Magic Links ✅ · Passkeys ✅ · MFA TOTP/SMS ✅ (SMS via Twilio/Vonage) · OAuth2/OIDC Provider ✅ · Social Login ✅ · RBAC + FGA ✅ · Webhooks ✅ · Policies & Actions ✅ · PKCE ✅ · Single Org ✅ · gRPC API ✅

**Enterprise:**
| Feature | Status | Note |
|---|---|---|
| SAML (SP + IdP) | ✅ | `SAMLIdentityLinker` + `SAMLSessionMinter` wired in `main.go` — SAML assertions now complete login with JIT provisioning (email matching + external_id linking). |
| SCIM provisioning | ✅ | Full CRUD + **token-issuing endpoint** (`POST /api/v1/scim/tokens`) + list/revoke — admin UI can issue bearer tokens. |
| Multi-org | ✅ | Gated correctly. |
| Audit export | ✅ | |
| SSO enforcement | ✅ | |
| Custom branding | ✅ | Backend + `GET /api/v1/public/branding` wired (enterprise build); the hosted login page is served by the binary and consumes it. (Admin UI to *edit* branding still seeds via API/DB.) |

## Panel 2 — Build architecture
| Element | Status | Note |
|---|---|---|
| OSS binary (`go build`) | ✅ | |
| Enterprise binary (`-tags enterprise`) | ✅ | `make build-enterprise` target exists — builds all 3 binaries with enterprise tag. |
| `requireFeature()` → 402 gate | ✅ | Works; tested. |

## Panel 3 — Licensing trust model
| Element | Status |
|---|---|
| Ed25519 sign / verify, embedded public key | ✅ (unit-tested) |
| License server endpoints (mint/revoke/revoked/list) | ✅ |
| Offline verification | ✅ |
| Revocation check | ✅ (tested with stub) |

**Panel 3 is the strongest part — solid and tested.** (License server internals not deeply re-audited here.)

## Panel 4 — Three commercial models
| Model | Status | Blocker |
|---|---|---|
| OSS | ✅ | build + run works |
| Enterprise (self-host) | 🟡 | SAML login wiring; no `build-enterprise` target; no published images |
| SaaS (cloud) | ❌ | see Panel 5 |

## Panel 5 — SaaS architecture
| Element | Status | Note |
|---|---|---|
| Multi-tenant core | ✅ | `internal/tenancy/` resolves instance→org |
| **Hosted login UI** | ✅ | Full source checked in (`hosted-ui/src/`): login (with social login buttons), registration, recovery, verification, **MFA** (TOTP + SMS + WebAuthn), **OAuth2 consent** screen. Branding-aware, dark theme. Build with `cd hosted-ui && npm install && npm run build`. |
| Custom domains / per-tenant TLS | ❌ | Not built |
| Plan-based gating (vs license token) | ❌ | Would need a small `license.Manager` adaptation |
| Billing (Stripe per-MAU) | ❌ | Not built |
| HA / backups / status page | ❌ | Ops not set up (graceful shutdown ✅ exists) |

**Panel 5 is mostly a target, not current state.**

## Panel 6 — Pricing tiers
📋 Business decision — a *proposal*, not code. Adjust freely.

## Panel 7 — End-to-end license flow
| Step | Status |
|---|---|
| Issue (`POST /licenses`) | ✅ |
| Activate (admin UI License page / `POST /api/v1/license`) | ✅ |
| Revoke (`DELETE` + revocation check) | ✅ |

**License lifecycle works end-to-end.** ✅

## Panel 8 — What the vendor must run
| Element | Status | Note |
|---|---|---|
| License server | ✅ | exists & runs |
| Key custody (HSM/Vault) | 📋/❌ | your ops setup — not done by code |
| Container registry / published images | ❌ | no released images yet |
| Billing portal | ❌ | optional; not built |

---

## Cross-cutting readiness (not on the graphic, but gates "production-ready")
| Area | Status |
|---|---|
| Unit test coverage | 🟡 — 40 of 55 packages untested (incl. `command`, `query`, `saml`, `api/http`) |
| Benchmarks / load test | ❌ — `BENCHMARKS.md` unmeasured |
| Live-DB integration run, real Okta/Entra federation test | ❌ |
| External security review | ❌ |
| SDKs | Go ✅ · Node ❌ · Python ❌ |

---

## Turning the 🟡/❌ green — shortest path

1. ~~**Wire SAML `linker`/`minter`**~~ ✅ DONE — `SAMLIdentityLinker` + `SAMLSessionMinter` wired in `main.go`.
2. ~~**Build the hosted login UI**~~ ✅ DONE — Full source with MFA/social/consent screens in `hosted-ui/src/`.
3. ~~**Add `make build-enterprise`**~~ ✅ Already exists in Makefile.
4. ~~**SCIM token-issuing endpoint + UI**~~ ✅ DONE — `POST /api/v1/scim/tokens` + list/revoke.
5. **Unit tests on core paths** — 40/55 packages still untested. Priority: `command`, `query`, `api/http`, `saml`.
6. **Publish OSS/ENT container images** → unblocks self-host distribution.
7. **P0 validations**: benchmarks, live DB, real Okta/Entra federation test, security review.

**One-line read:** *SAML, SCIM, hosted UI, and enterprise build are all green. Remaining work: unit test coverage, container images, and production validations.*
