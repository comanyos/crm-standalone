import { test, expect } from "@playwright/test";
import { appOrigin, authPortalUrl, otpSignIn, stubAuthPortal } from "./helpers";

// Authentication itself must start without the setup project's saved cookie.
test.use({ storageState: { cookies: [], origins: [] } });

test("log in with an email code, reload the session, and log out", async ({ page }) => {
  await stubAuthPortal(page);
  await page.goto("/");

  await expect(page).toHaveURL((url) => {
    const portal = authPortalUrl();
    return url.origin === portal.origin && url.searchParams.get("auth") === "login" && url.searchParams.get("returnTo") === "/";
  });

  await otpSignIn(page);

  await expect(page).toHaveURL(new RegExp(`^${appOrigin().replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/?$`));
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
  await expect(page.getByText("Total Records")).toBeVisible();

  // GET /auth/session restores the UI from the HttpOnly cookie.
  await page.reload();
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();

  await page.getByRole("button", { name: "Account menu" }).click();
  await page.getByRole("menuitem", { name: "Sign out" }).click();
  await expect(page.getByRole("heading", { name: "You're signed out" })).toBeVisible();

  await page.goto("/");
  await expect(page).toHaveURL((url) => url.origin === authPortalUrl().origin && url.searchParams.get("auth") === "login");
});

test("an unauthenticated protected route preserves its return path", async ({ page }) => {
  await stubAuthPortal(page);
  await page.goto("/accounts");
  await expect(page).toHaveURL((url) => {
    return url.origin === authPortalUrl().origin && url.searchParams.get("auth") === "login" && url.searchParams.get("returnTo") === "/accounts";
  });
});
