import { test as setup, expect } from "@playwright/test";
import { AUTH_STATE_PATH, otpSignIn } from "./helpers";

/**
 * Signs in once per run and saves the HttpOnly session cookie for every
 * other spec. auth.spec.ts deliberately uses a clean context to exercise the
 * login journey itself.
 */
setup("authenticate once for the whole run", async ({ page }) => {
  await otpSignIn(page);
  await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
  await page.context().storageState({ path: AUTH_STATE_PATH });
});
