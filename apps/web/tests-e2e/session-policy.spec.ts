import { expect, test } from "@playwright/test";
import { openApp } from "./helpers";

/**
 * Session policy in a real browser (task.md §1.2).
 *
 * The unit tests in apps/web/src/auth/ drive these paths against a mocked
 * auth API, which is the right place to assert the state machine. What
 * they cannot show is that the wiring survives a real document: that a
 * BroadcastChannel actually crosses two pages of the same origin and that an
 * HttpOnly cookie restores the session without exposing a token to JS.
 *
 * Both specs run against the live stack, like the rest of this directory.
 */

test.describe("session policy", () => {
  /**
   * Two tabs of the same origin: the sign-out broadcast one makes must end
   * the other, and the ended tab must say so rather than sitting on a
   * protected route with nothing to show.
   *
   * The broadcast is posted directly because a real logout would revoke the
   * saved session shared by parallel specs. auth.spec.ts owns real logout;
   * this test owns cross-document propagation.
   */
  test("a sign-out broadcast from one tab ends the session in another", async ({ context }) => {
    const signingOut = await context.newPage();
    const listening = await context.newPage();
    await openApp(signingOut);
    await openApp(listening);

    await signingOut.evaluate(() => {
      const channel = new BroadcastChannel("crm_auth_sign_out");
      channel.postMessage({ type: "signed-out", at: Date.now() });
      channel.close();
    });

    // The listening tab was never navigated: it hears about this over the
    // channel, and RequireAuth sends it to the confirmation rather than
    // leaving it on "Redirecting to sign in…" forever.
    await expect(listening).toHaveURL(/\/signed-out$/);
    await expect(listening.getByRole("heading", { name: "You're signed out" })).toBeVisible();

    await signingOut.close();
    await listening.close();
  });

  test("the HttpOnly session survives a reload", async ({ page }) => {
    await openApp(page);
    await page.reload();
    await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
  });

  /**
   * No session credential may reach storage readable by JavaScript. The
   * browser sends the HttpOnly cookie without exposing its value to the app.
   */
  test("no access token is written to browser storage", async ({ page }) => {
    await openApp(page);

    const stored = await page.evaluate(() => {
      const dump = (storage: Storage) => {
        const out: Record<string, string> = {};
        for (let i = 0; i < storage.length; i++) {
          const key = storage.key(i);
          if (key) out[key] = storage.getItem(key) ?? "";
        }
        return out;
      };
      return { local: dump(window.localStorage), session: dump(window.sessionStorage) };
    });

    expect(Object.keys(stored.session)).toHaveLength(0);
    expect(Object.keys(stored.local)).toHaveLength(0);
  });
});
