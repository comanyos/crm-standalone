import { test, expect } from "@playwright/test";
import {
  AUTH_STATE_PATH,
  createAccount,
  deleteFromDetailPage,
  expectOnDetailPage,
  openApp,
  uniqueName,
} from "./helpers";

/** The sidebar link, not the breadcrumb — both are labelled "Accounts". */
const accountsNavLink = (page: import("@playwright/test").Page) =>
  page.getByLabel("Primary").getByRole("link", { name: "Accounts" });

/**
 * The Accounts journey against a live backend: create, read, search, edit,
 * the unsaved-changes guard, and delete. Each test signs in fresh and
 * cleans up the records it created, so the development database it runs
 * against doesn't accumulate fixtures.
 */
test.beforeEach(async ({ page }) => {
  await openApp(page);
});

test("create, find, edit and delete an account", async ({ page }) => {
  const name = uniqueName("E2E Account");

  await accountsNavLink(page).click();
  // exact: true — Playwright matches accessible names as substrings by
  // default, and the empty state's "No accounts yet" heading contains it.
  await expect(page.getByRole("heading", { name: "Accounts", exact: true })).toBeVisible();

  // Create.
  // On a tenant with no accounts yet the empty state offers its own
  // identically-labelled action alongside the page header's, so this picks
  // the header one rather than depending on how much data happens to exist.
  await page.getByRole("button", { name: "New account" }).first().click();
  await page.getByLabel(/^Name/).fill(name);
  await page.getByLabel("Owner ID").fill("e2e-owner");
  await page.getByLabel("Industry").fill("Robotics");
  await page.getByLabel("Website").fill("https://e2e.example");
  await page.getByRole("button", { name: "Create account" }).click();

  // Lands on the new account's detail page — and does NOT ask to discard
  // the changes it just saved.
  await expectOnDetailPage(page, name);
  await expect(page.getByText("Discard unsaved changes?")).toBeHidden();
  await expect(page.getByText("Robotics")).toBeVisible();
  const detailUrl = page.url();

  // Find it again through the server-side name filter.
  await accountsNavLink(page).click();
  await page.getByLabel("Search by name").fill(name);
  await expect(page.getByRole("cell", { name })).toBeVisible();

  // Edit.
  await page.goto(detailUrl);
  await page.getByRole("button", { name: "Edit" }).click();
  await expect(page.getByLabel(/^Name/)).toHaveValue(name);
  await page.getByLabel("Industry").fill("Aerospace");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expectOnDetailPage(page, name);
  await expect(page.getByText("Aerospace")).toBeVisible();

  // Delete, with confirmation.
  await page.getByRole("button", { name: "Delete" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText(`Delete ${name}? This cannot be undone.`)).toBeVisible();
  await dialog.getByRole("button", { name: "Delete" }).click();

  await expect(page).toHaveURL(/\/accounts$/);
  await page.getByLabel("Search by name").fill(name);
  await expect(page.getByText("No matching accounts")).toBeVisible();
});

test("validation errors are shown before anything is sent", async ({ page }) => {
  await page.goto("/accounts/new");

  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByText("Name is required")).toBeVisible();

  await page.getByLabel(/^Name/).fill(uniqueName("E2E Invalid"));
  await page.getByLabel("Owner ID").fill("e2e-owner");
  await page.getByLabel("Website").fill("not-a-url");
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByText("Website must start with http:// or https://")).toBeVisible();

  // Still on the form: nothing was created.
  await expect(page).toHaveURL(/\/accounts\/new$/);
});

test("leaving a half-filled form asks before discarding", async ({ page }) => {
  await page.goto("/accounts/new");
  await page.getByLabel(/^Name/).fill("Half typed");

  await accountsNavLink(page).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("Discard unsaved changes?")).toBeVisible();

  // Cancel keeps both the page and the typed value.
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(page).toHaveURL(/\/accounts\/new$/);
  await expect(page.getByLabel(/^Name/)).toHaveValue("Half typed");

  // Discard actually leaves.
  await accountsNavLink(page).click();
  await page.getByRole("button", { name: "Discard changes" }).click();
  await expect(page).toHaveURL(/\/accounts$/);
});

test("an unknown account id shows the tenant-safe not-found state", async ({ page }) => {
  await page.goto("/accounts/does-not-exist");

  await expect(page.getByText("Not found")).toBeVisible();
  await expect(page.getByText("This record doesn't exist, or you don't have access to it.")).toBeVisible();
});

test("saving a stale edit reports the conflict instead of overwriting", async ({ page, browser }) => {
  const name = uniqueName("E2E Conflict");
  const url = await createAccount(page, name);

  // This page loads the record — and with it the updated_at that its next
  // save will send back.
  await page.goto(`${url}/edit`);
  await expect(page.getByLabel(/^Name/)).toHaveValue(name);

  // Meanwhile a second, fully independent session saves a change first.
  // Explicit storageState: a context created here does not inherit the
  // project's, and re-entering the password would hit AuthinCore's login
  // rate limit (see tests-e2e/auth.setup.ts).
  const otherContext = await browser.newContext({ storageState: AUTH_STATE_PATH });
  const other = await otherContext.newPage();
  await openApp(other);
  await other.goto(`${url}/edit`);
  await other.getByLabel("Industry").fill("Changed elsewhere");
  await other.getByRole("button", { name: "Save changes" }).click();
  // Landing on the detail page is what proves the write finished — closing
  // the context while the request is still in flight cancels it, and the
  // first page's updated_at would then still be current, so there would be
  // no conflict for the assertion below to find.
  await expectOnDetailPage(other, name);
  await otherContext.close();

  // The first page's updated_at is now stale, so the server must refuse the
  // write (ErrConflict → connect Aborted) rather than silently clobbering
  // the other session's change.
  await page.getByLabel("Industry").fill("Changed here");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(
    page.getByText("This record was changed by someone else. Reload the page and try again."),
  ).toBeVisible();

  // The other session's value survived. (A page.goto is a full browser
  // navigation, which useBlocker does not intercept — only the
  // beforeunload half of the guard applies, and Playwright dismisses that
  // automatically.)
  await page.goto(url);
  await expect(page.getByText("Changed elsewhere")).toBeVisible();

  await deleteFromDetailPage(page, url);
});

test("an account sharing a website domain is flagged as a possible duplicate", async ({ page }) => {
  const website = `https://dup-${Date.now()}.example`;
  const firstName = uniqueName("E2E Dup A");
  const secondName = uniqueName("E2E Dup B");

  const firstUrl = await createAccount(page, firstName);
  await page.goto(`${firstUrl}/edit`);
  // Wait for the form to be populated from the server before typing: it
  // renders empty and fills in when the record arrives, so an early fill
  // gets overwritten by that reset and the website never gets saved.
  await expect(page.getByLabel(/^Name/)).toHaveValue(firstName);
  await page.getByLabel("Website").fill(website);
  await page.getByRole("button", { name: "Save changes" }).click();
  // The website has to be committed before the second account is created,
  // or there is nothing for the dedup scan to match. The detail page's own
  // rendering of the saved value is the proof: the navigation below cancels
  // anything still in flight.
  await expectOnDetailPage(page, firstName);
  await expect(page.getByRole("link", { name: website })).toBeVisible();

  await page.goto("/accounts/new");
  await page.getByLabel(/^Name/).fill(secondName);
  await page.getByLabel("Owner ID").fill("e2e-owner");
  await page.getByLabel("Website").fill(website);
  await page.getByRole("button", { name: "Create account" }).click();

  // Flagged, not blocked and never merged: the account was still created.
  await expectOnDetailPage(page, secondName);
  await expect(page.getByRole("link", { name: "another account" })).toBeVisible();
  await expect(page.getByRole("button", { name: /merge/i })).toBeHidden();
  const secondUrl = page.url();

  await deleteFromDetailPage(page, secondUrl);
  await deleteFromDetailPage(page, firstUrl);
});
