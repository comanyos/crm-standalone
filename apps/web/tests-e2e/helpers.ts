import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, type Page } from "@playwright/test";

/** Where auth.setup.ts saves the signed-in browser state that every other
 * spec reuses. Gitignored — it holds a live session cookie. */
export const AUTH_STATE_PATH = path.join(path.dirname(fileURLToPath(import.meta.url)), ".auth", "state.json");

/**
 * Local-development bootstrap credentials (apps/web/README.md). These are
 * the documented defaults `make bootstrap` creates on a developer machine —
 * override them with E2E_ADMIN_EMAIL / E2E_ADMIN_PASSWORD for any stack that
 * isn't a throwaway local one. Never point this at a shared environment.
 */
export const ADMIN_EMAIL = process.env.E2E_ADMIN_EMAIL ?? "admin@localhost";
export const ADMIN_PASSWORD = process.env.E2E_ADMIN_PASSWORD ?? "use-a-unique-12-character-minimum-password";

/** Drives AuthinCore's hosted login with a password and waits for the app
 * to take over. Used by auth.setup.ts once per run, and by auth.spec.ts,
 * which owns asserting the details of the PKCE flow itself. Most specs
 * should not call this — they inherit the saved session instead, because
 * AuthinCore rate-limits password logins per IP (see auth.setup.ts). */
export async function passwordSignIn(page: Page) {
  await page.goto("/");
  await expect(page).toHaveURL(/localhost:18080/);

  // AuthinCore offers several sign-in methods as tabs and does not always
  // render them in the same order, so which one starts selected varies
  // between loads. Select the password tab explicitly and wait for its form
  // — a non-retrying isVisible() check here silently skips the click when
  // the page hasn't painted yet, leaving a different method selected.
  const passwordTab = page.getByRole("tab", { name: "password" });
  await expect(passwordTab).toBeVisible();
  await passwordTab.click();
  await expect(page.getByLabel("Identifier")).toBeVisible();

  await page.getByLabel("Identifier").fill(ADMIN_EMAIL);
  await page.getByLabel("Password").fill(ADMIN_PASSWORD);
  await page.getByRole("button", { name: "Continue" }).click();

  const allowButton = page.getByRole("button", { name: "Allow" });
  if (await allowButton.isVisible({ timeout: 5000 }).catch(() => false)) {
    await allowButton.click();
  }

  await expect(page).toHaveURL(/localhost:3000\/?$/);
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
}

/** A name unique to this run. These tests write to a live development
 * database, so fixtures must never collide with a previous run's leftovers
 * (or with a parallel worker's). */
export function uniqueName(prefix: string) {
  return `${prefix} ${Date.now()}-${Math.floor(Math.random() * 10000)}`;
}

/** Creates an account through the UI and returns its detail-page URL. */
export async function createAccount(page: Page, name: string) {
  await page.goto("/accounts/new");
  await page.getByLabel(/^Name/).fill(name);
  await page.getByLabel("Owner ID").fill("e2e-owner");
  await page.getByRole("button", { name: "Create account" }).click();
  await expectOnDetailPage(page, name);
  return page.url();
}

/**
 * Waits for a save to have actually landed: the form page navigates to the
 * record's detail page only in the mutation's onSuccess, so reaching that
 * page is the proof the write completed.
 *
 * exact: true is the whole point. Playwright matches accessible names as
 * substrings, and the form pages' own header is `Edit <name>` — so a plain
 * `getByRole("heading", { name })` resolves instantly against the page the
 * test is still on, before the save's response arrives. The test then walks
 * on and its next page.goto cancels the in-flight request, losing the write
 * silently. That is what made the duplicate-website spec fail under
 * parallel workers (docs/ROADMAP.md Phase 3.2).
 */
export async function expectOnDetailPage(page: Page, name: string) {
  await expect(page.getByRole("heading", { name, exact: true })).toBeVisible();
}

/** Deletes whatever record the given detail page shows, through the
 * confirmation dialog. Used to clean up fixtures from the live development
 * database these tests run against. */
export async function deleteFromDetailPage(page: Page, url: string) {
  await page.goto(url);
  await page.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
  // Wait for the delete to land before returning. The dialog closes as soon
  // as it is confirmed, while the request is still in flight; a caller that
  // immediately navigates elsewhere (the usual "delete both fixtures" pair)
  // cancels it and leaves the record behind. The detail page navigates to
  // the list only in the mutation's onSuccess, so this URL is the receipt.
  const listPath = new URL(url).pathname.split("/").slice(0, -1).join("/");
  await expect(page).toHaveURL(new RegExp(`${listPath}$`));
}

/** Opens the app as the already-signed-in user from the saved storage
 * state, completing the OIDC authorize round-trip against AuthinCore's
 * existing session rather than re-entering a password. */
export async function openApp(page: Page) {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
}
