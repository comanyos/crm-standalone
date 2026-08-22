import { expect, test } from "@playwright/test";
import { createAccount, deleteFromDetailPage, openApp, uniqueName } from "./helpers";

/**
 * Tasks and notes against the live stack (task.md §5.1).
 *
 * These write to the development database, so every fixture is uniquely
 * named and removed at the end — a task or note left behind would show up in
 * a later run's "my open tasks" and make it fail for a reason unrelated to
 * what it tests.
 */

test("create a task, find it in the work list, and complete it", async ({ page }) => {
  const title = uniqueName("E2E task");
  await openApp(page);

  await page.goto("/tasks/new");
  await page.getByLabel(/^Title/).fill(title);
  await page.getByLabel("Priority").selectOption("high");
  await page.getByRole("button", { name: "Create task" }).click();

  // Lands on the work list, which opens on the reader's own open tasks — and
  // the task was assigned to them by default, so it is there without
  // switching views.
  //
  // Located by the completion control's own label rather than by row: the
  // table marks an interactive row role="button", which overrides the
  // implicit row role, so getByRole("row") finds nothing here.
  await expect(page).toHaveURL(/\/tasks$/);
  await expect(page.getByText(title)).toBeVisible();

  // Completing from the list uses the dedicated call, so no read-modify-write
  // of the whole record is involved.
  // click, not check: the control is controlled by the task's status, and
  // the row leaves this view the moment it completes — so it never ends up
  // in the "checked" state check() waits for.
  await page.getByRole("checkbox", { name: `Complete ${title}` }).click();
  await expect(page.getByText("Task completed.")).toBeVisible();

  // It leaves the default view, because that view is open tasks only.
  await expect(page.getByText(title)).toBeHidden();

  // And is still there, completed, under All tasks — where it can be reopened.
  await page.getByRole("tab", { name: "All tasks" }).click();
  await expect(page.getByText(title)).toBeVisible();
  await page.getByRole("checkbox", { name: `Reopen ${title}` }).click();
  await expect(page.getByText("Task reopened.")).toBeVisible();
});

test("add a task from an account and complete it there", async ({ page }) => {
  const accountName = uniqueName("E2E task account");
  await openApp(page);
  const accountUrl = await createAccount(page, accountName);

  await expect(page.getByRole("heading", { name: "Open tasks" })).toBeVisible();
  await expect(page.getByText("Nothing outstanding.")).toBeVisible();

  const title = uniqueName("Call procurement");
  await page.getByRole("button", { name: "Add task" }).click();
  await page.getByLabel(/^Title/).fill(title);
  await page.getByRole("button", { name: "Create task" }).click();

  // Saving returns to the record the task belongs to, not to the work list —
  // the reader came from here.
  await expect(page).toHaveURL(new RegExp(accountUrl.split("/").pop() ?? ""));
  await expect(page.getByText(title)).toBeVisible();

  await page.getByRole("button", { name: "Complete" }).click();
  await expect(page.getByText("Task completed.")).toBeVisible();
  // A record page shows what is outstanding; a completed task is history.
  await expect(page.getByText("Nothing outstanding.")).toBeVisible();

  await deleteFromDetailPage(page, accountUrl);
});

test("write, correct and remove a note on an account", async ({ page }) => {
  const accountName = uniqueName("E2E note account");
  await openApp(page);
  const accountUrl = await createAccount(page, accountName);

  // Scoped to the notes section throughout: the account page has its own
  // Edit and Delete buttons, and an unscoped getByRole would resolve to
  // those and navigate away from the thing under test.
  const notes = page.locator("section").filter({ has: page.getByRole("heading", { name: "Notes" }) });
  await expect(notes.getByRole("heading", { name: "Notes" })).toBeVisible();
  await expect(notes.getByText("No notes yet.")).toBeVisible();

  await notes.getByRole("button", { name: "Add note" }).click();
  await page.getByRole("textbox", { name: "Note", exact: true }).fill("Renewal owner is Priya.");
  await page.getByRole("button", { name: "Save note" }).click();
  await expect(notes.getByText("Renewal owner is Priya.")).toBeVisible();

  // A brand-new note is not marked as edited.
  await expect(notes.getByText(/edited/i)).toBeHidden();

  await notes.getByRole("button", { name: "Edit note" }).click();
  await page.getByRole("textbox", { name: "Note", exact: true }).fill("Renewal owner is Priya (confirmed).");
  await page.getByRole("button", { name: "Save changes" }).click();

  await expect(notes.getByText("Renewal owner is Priya (confirmed).")).toBeVisible();
  // A note is mutable, so the reader is told it was corrected.
  await expect(notes.getByText(/edited/i)).toBeVisible();

  await notes.getByRole("button", { name: "Delete note" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
  await expect(notes.getByText("No notes yet.")).toBeVisible();

  await deleteFromDetailPage(page, accountUrl);
});
