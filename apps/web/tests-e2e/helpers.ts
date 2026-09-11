import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, type Page } from "@playwright/test";

/** The app's own origin, as configured for this machine. Playwright's
 * baseURL is derived from the same env (playwright.config.ts), so tests that
 * need to name the origin explicitly stay in step with it. */
export function appOrigin(): string {
  return process.env.PLAYWRIGHT_APP_ORIGIN ?? `http://localhost:${process.env.VITE_WEB_PORT ?? "3000"}`;
}

export function apiOrigin(): string {
  return process.env.PLAYWRIGHT_API_ORIGIN ?? "http://localhost:19431";
}

export function authPortalUrl(): URL {
  return new URL(process.env.PLAYWRIGHT_AUTH_PORTAL_URL ?? "http://localhost:4173/?auth=login#top");
}

function escapeForRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** Where auth.setup.ts saves the signed-in browser state that every other
 * spec reuses. Gitignored — it holds a live session cookie. */
export const AUTH_STATE_PATH = path.join(path.dirname(fileURLToPath(import.meta.url)), ".auth", "state.json");

/**
 * Local development account and development-only OTP. The API must be
 * started with AUTH_DEV_OTP matching E2E_LOGIN_CODE; production and staging
 * reject that server setting.
 */
export const ADMIN_EMAIL = process.env.E2E_ADMIN_EMAIL ?? "admin@localhost";
export const LOGIN_CODE = process.env.E2E_LOGIN_CODE ?? "123456";

/** Creates the first-party passwordless session through the real API and then
 * waits for the app dashboard. The combined Login / Sign-up UI lives in the
 * separately deployed marketing artifact, so this app suite tests its
 * redirect contract while that artifact owns its UI acceptance test. */
export async function otpSignIn(page: Page) {
  const requestCode = await page.request.post(`${apiOrigin()}/auth/email/request`, {
    data: { email: ADMIN_EMAIL, flow: "login" },
  });
  expect(requestCode.ok()).toBeTruthy();
  const challenge = (await requestCode.json()) as { challengeId: string };

  const verifyCode = await page.request.post(`${apiOrigin()}/auth/email/verify`, {
    data: { challengeId: challenge.challengeId, code: LOGIN_CODE },
  });
  expect(verifyCode.ok()).toBeTruthy();

  await page.goto("/");
  await expect(page).toHaveURL(new RegExp(`^${escapeForRegExp(appOrigin())}/?$`));
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
}

/** Prevents a CRM-app test from depending on a separately checked-out static
 * website while preserving the exact cross-origin redirect URL for assertion. */
export async function stubAuthPortal(page: Page) {
  const portal = authPortalUrl();
  await page.route(`${portal.origin}/**`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "text/html",
      body: "<!doctype html><title>Savvy CRM sign in</title><h1>Savvy CRM sign in</h1>",
    }),
  );
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

/** Opens the app with the saved HttpOnly session cookie. */
export async function openApp(page: Page) {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
}
