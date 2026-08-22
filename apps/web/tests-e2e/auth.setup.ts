import { test as setup, expect } from "@playwright/test";
import { AUTH_STATE_PATH, passwordSignIn } from "./helpers";

/**
 * Signs in once per run and saves the resulting browser state for every
 * other spec to reuse.
 *
 * AuthinCore rate-limits password logins per client IP — not per user — in a
 * `rl:login:<org>:<ip>` counter in Redis (brute-force protection working as
 * intended; see docs/CONFIGURATION.md). Every worker on this machine shares
 * that one bucket, so a suite where each test logs in with a password trips
 * the limit and starts failing at sign-in, which looks like a product bug but
 * isn't. Giving each worker its own identity would not help, for the same
 * reason. Reusing AuthinCore's session cookie means each test still performs
 * a full OIDC authorize round-trip — which is not rate-limited — it just
 * doesn't re-enter the password.
 *
 * A throttled login is easy to misread: the browser is left sitting on
 * `/login?return_to=%2Foauth2%2Fauthorize...`, the same URL an
 * unauthenticated authorize request redirects to, so it looks like the
 * session was rejected rather than the login never being attempted. The
 * difference is visible in `docker compose logs authincore`: a throttled
 * login is `POST /self-service/login` → 429.
 *
 * auth.spec.ts deliberately opts out of this and drives the password flow
 * itself, since that flow is what it exists to test.
 */
setup("authenticate once for the whole run", async ({ page }) => {
  await passwordSignIn(page);
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
  await page.context().storageState({ path: AUTH_STATE_PATH });
});
