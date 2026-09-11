import { test, expect, type Page } from "@playwright/test";
import { createAccount, deleteFromDetailPage, openApp, uniqueName } from "./helpers";

/**
 * The Contacts journey against a live backend. Contacts can't exist without
 * an account, so each test creates its own account first and removes both
 * records at the end.
 */
const contactsNavLink = (page: Page) =>
  page.getByLabel("Primary").getByRole("link", { name: "Contacts", exact: true });

test.beforeEach(async ({ page }) => {
  await openApp(page);
});

test("create a contact via the account picker, edit it, then delete it", async ({ page }) => {
  const accountName = uniqueName("E2E Contact Account");
  const accountUrl = await createAccount(page, accountName);
  const email = `e2e-${Date.now()}@example.test`;

  await contactsNavLink(page).click();
  // On a tenant with no contacts yet the empty state offers its own
  // identically-labelled action alongside the page header's, so this picks
  // the header one rather than depending on how much data happens to exist.
  await page.getByRole("button", { name: "New contact" }).first().click();

  await page.getByLabel(/^Email/).fill(email);

  // The account combobox searches the live backend by name and only ever
  // commits an id it got back — there is no free-text account.
  const picker = page.getByRole("combobox", { name: "Account" });
  await picker.click();
  await picker.fill(accountName);
  await page.getByRole("button", { name: accountName }).click();

  await page.getByLabel("First name").fill("Grace");
  await page.getByLabel("Last name").fill("Hopper");
  await page.getByLabel("Role").selectOption("technical");
  await page.getByRole("button", { name: "Create contact" }).click();

  await expect(page.getByRole("heading", { name: "Grace Hopper", exact: true })).toBeVisible();
  await expect(page.getByText("Discard unsaved changes?")).toBeHidden();
  await expect(page.getByRole("link", { name: accountName })).toBeVisible();
  const contactUrl = page.url();

  // Find it again through the server-side email filter.
  await contactsNavLink(page).click();
  await page.getByLabel("Search by email").fill(email);
  // The first/naming column is a semantic row header, not a data cell. Scope
  // it to this run's unique email so fixtures stranded by an interrupted old
  // run cannot make the name alone ambiguous.
  const savedRow = page.getByRole("row").filter({ hasText: email });
  await expect(savedRow.getByRole("rowheader", { name: "Grace Hopper" })).toBeVisible();

  // Edit.
  await page.goto(contactUrl);
  await page.getByRole("button", { name: "Edit" }).click();
  await expect(page.getByLabel(/^Email/)).toHaveValue(email);
  await page.getByLabel("Title").fill("Rear Admiral");
  await page.getByRole("button", { name: "Save changes" }).click();
  // exact: true — the edit page's own header is `Edit Grace Hopper`, which a
  // substring match resolves against before the save's response arrives (see
  // expectOnDetailPage in helpers.ts).
  await expect(page.getByRole("heading", { name: "Grace Hopper", exact: true })).toBeVisible();
  await expect(page.getByText("Rear Admiral")).toBeVisible();

  await deleteFromDetailPage(page, contactUrl);
  await expect(page).toHaveURL(/\/contacts$/);
  await deleteFromDetailPage(page, accountUrl);
});

test("the Add contact shortcut carries the account across from the account page", async ({ page }) => {
  const accountName = uniqueName("E2E Shortcut Account");
  const accountUrl = await createAccount(page, accountName);

  await page.getByRole("button", { name: "Add contact" }).click();

  // The picker arrives already showing the account it came from.
  await expect(page.getByRole("combobox", { name: "Account" })).toHaveValue(accountName);

  const email = `e2e-shortcut-${Date.now()}@example.test`;
  await page.getByLabel(/^Email/).fill(email);
  await page.getByRole("button", { name: "Create contact" }).click();

  await expect(page.getByRole("heading", { name: email })).toBeVisible();
  const contactUrl = page.url();

  // The account page now lists the contact that was created from it.
  await page.goto(accountUrl);
  await expect(page.getByRole("link", { name: email })).toBeVisible();

  await deleteFromDetailPage(page, contactUrl);
  await deleteFromDetailPage(page, accountUrl);
});

test("a contact cannot be saved without a valid email and a real account", async ({ page }) => {
  await page.goto("/contacts/new");

  await page.getByRole("button", { name: "Create contact" }).click();
  await expect(page.getByText("Email is required")).toBeVisible();
  await expect(page.getByText("Select an account")).toBeVisible();

  await page.getByLabel(/^Email/).fill("not-an-email");
  await page.getByRole("button", { name: "Create contact" }).click();
  await expect(page.getByText("Enter a valid email address")).toBeVisible();

  await expect(page).toHaveURL(/\/contacts\/new$/);
});

test("an unknown contact id shows the tenant-safe not-found state", async ({ page }) => {
  await page.goto("/contacts/does-not-exist");

  await expect(page.getByText("Not found")).toBeVisible();
  await expect(page.getByText("This record doesn't exist, or you don't have access to it.")).toBeVisible();
});

test("a second contact with the same email is flagged, not blocked or merged", async ({ page }) => {
  const accountName = uniqueName("E2E Dup Contact Account");
  const accountUrl = await createAccount(page, accountName);
  const email = `e2e-dup-${Date.now()}@example.test`;

  async function addContact(firstName: string) {
    await page.goto(`/contacts/new?accountId=${new URL(accountUrl).pathname.split("/").pop()}`);
    await page.getByLabel(/^Email/).fill(email);
    const picker = page.getByRole("combobox", { name: "Account" });
    await picker.click();
    await picker.fill(accountName);
    await page.getByRole("button", { name: accountName }).click();
    await page.getByLabel("First name").fill(firstName);
    await page.getByRole("button", { name: "Create contact" }).click();
    await expect(page.getByRole("heading", { name: new RegExp(firstName) })).toBeVisible();
    return page.url();
  }

  const firstUrl = await addContact("Ada");
  const secondUrl = await addContact("Grace");

  // The save went through and the user is warned — never silently merged.
  await expect(page.getByText(/This email matches/)).toBeVisible();
  await expect(page.getByRole("button", { name: /merge/i })).toBeHidden();

  await deleteFromDetailPage(page, secondUrl);
  await deleteFromDetailPage(page, firstUrl);
  await deleteFromDetailPage(page, accountUrl);
});
