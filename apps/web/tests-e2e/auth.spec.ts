import { test, expect } from "@playwright/test";

// This spec is the one that exercises the password sign-in and sign-out
// journey itself, so it starts from a clean browser rather than the session
// auth.setup.ts saved for everything else.
test.use({ storageState: { cookies: [], origins: [] } });

/**
 * Exercises the full Authorization Code + PKCE journey against a live
 * backend: sign in with the bootstrap administrator, land on the protected
 * dashboard, reload while the session is valid, and sign out.
 *
 * Prerequisites (see apps/web/README.md): `make up`, then `make bootstrap`
 * with credentials matching ADMIN_EMAIL/ADMIN_PASSWORD below, and
 * apps/web/.env populated with that org's VITE_AUTH_ORG_ID.
 */
const ADMIN_EMAIL = process.env.E2E_ADMIN_EMAIL ?? "admin@localhost";
const ADMIN_PASSWORD = process.env.E2E_ADMIN_PASSWORD ?? "use-a-unique-12-character-minimum-password";

test("sign in, reach the dashboard, reload, and sign out", async ({ page }) => {
  await page.goto("/");

  // RequireAuth redirects to AuthinCore's hosted login page.
  await expect(page).toHaveURL(/localhost:18080/);
  await expect(page.getByRole("heading", { name: "Welcome back" })).toBeVisible();

  // Selected explicitly and waited for: the sign-in method tabs are not
  // always rendered in the same order, and a non-retrying isVisible() check
  // skips the click when the page hasn't painted yet (see helpers.ts).
  const passwordTab = page.getByRole("tab", { name: "password" });
  await expect(passwordTab).toBeVisible();
  await passwordTab.click();
  await expect(page.getByLabel("Identifier")).toBeVisible();

  await page.getByLabel("Identifier").fill(ADMIN_EMAIL);
  await page.getByLabel("Password").fill(ADMIN_PASSWORD);
  await page.getByRole("button", { name: "Continue" }).click();

  // First-ever login for this client hits the consent screen; a remembered
  // grant from a prior run skips straight to the app. Handle both.
  const allowButton = page.getByRole("button", { name: "Allow" });
  if (await allowButton.isVisible({ timeout: 5000 }).catch(() => false)) {
    await allowButton.click();
  }

  // Lands back on the app's protected dashboard with live backend data.
  await expect(page).toHaveURL(/localhost:3000\/?$/);
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
  await expect(page.getByText("Total Records")).toBeVisible();

  // Reload while the session is valid — must not bounce back to login.
  await page.reload();
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();

  // Sign out via the user menu, then confirm the session actually ended.
  // No response-waiting needed here: AuthProvider.logout holds the page on
  // "Signing out…" until the revoke and the session DELETE have landed, so
  // the heading below is itself the signal that the server side is done.
  await page.getByRole("button", { name: "Account menu" }).click();
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await expect(page.getByRole("heading", { name: "You're signed out" })).toBeVisible();

  // /signed-out itself is a public route and would just re-render on
  // reload — visit a protected route instead to prove the session (and its
  // refresh token) is actually gone, not just hidden by client-side state.
  await page.goto("/");
  await expect(page).toHaveURL(/localhost:18080/);
});

test("an unauthenticated visit to a protected route redirects to sign in", async ({ page }) => {
  await page.goto("/accounts");
  await expect(page).toHaveURL(/localhost:18080/);
});
