import { expect, test } from "@playwright/test";
import { openApp } from "./helpers";

/**
 * Session policy in a real browser (task.md §1.2).
 *
 * The unit tests in apps/web/src/auth/ drive these paths against a mocked
 * auth client, which is the right place to assert the state machine. What
 * they cannot show is that the wiring survives a real document: that a
 * BroadcastChannel actually crosses two pages of the same origin, and that a
 * timed-out session really is gone after a reload rather than being restored
 * from the refresh token still sitting in sessionStorage.
 *
 * Both specs run against the live stack, like the rest of this directory.
 */

test.describe("session policy", () => {
  /**
   * Two tabs of the same origin: the sign-out broadcast one makes must end
   * the other, and the ended tab must say so rather than sitting on a
   * protected route with nothing to show.
   *
   * The broadcast is posted directly rather than by clicking Sign out, and
   * that is a deliberate limit on this test's scope. A real sign-out ends
   * AuthinCore's session, and every other spec in this directory shares that
   * one session through the saved storage state (auth.setup.ts) — so a test
   * that signs out for real signs every parallel worker out too, which is
   * exactly what it did before this comment existed. auth.spec.ts owns the
   * real sign-out flow, and the unit tests own "logout broadcasts". What is
   * left for this tier, and only reachable here, is whether the message
   * crosses two real documents and what the receiving tab then shows.
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

  /**
   * A timed-out session must not come back on reload.
   *
   * The refresh token lives in sessionStorage, which survives a reload by
   * design — that is what makes "still signed in after F5" work. So an idle
   * timeout that only cleared React state would be undone by the next
   * refresh, and the user would be told they had been signed out while still
   * being signed in.
   *
   * The timeout itself is 30 minutes, which no browser test should wait for.
   * What is checked here is the consequence that matters and cannot be seen
   * from a unit test: with the refresh token gone, a reload lands on the
   * signed-out experience rather than the dashboard.
   */
  test("a session ended locally does not survive a reload", async ({ page }) => {
    await openApp(page);

    // Stand in for the idle timeout by removing exactly what it removes.
    // Driving the real 30-minute timer is out of scope for a browser test;
    // asserting the app cannot resurrect a session without that token is
    // the part a unit test cannot reach.
    await page.evaluate(() => window.sessionStorage.removeItem("crm_auth_refresh_token"));

    await page.reload();

    // No dashboard: with no refresh token there is nothing to renew from,
    // and the app must ask for a sign-in rather than silently continuing.
    await expect(page.getByRole("heading", { name: "Dashboard" })).toBeHidden();
  });

  /**
   * The complement of the test above, so it cannot pass merely because the
   * dashboard is slow to render: with the refresh token in place, a reload
   * does restore the session.
   */
  test("a live session does survive a reload", async ({ page }) => {
    await openApp(page);

    const stored = await page.evaluate(() =>
      window.sessionStorage.getItem("crm_auth_refresh_token"),
    );
    expect(stored, "the app should hold a refresh token in sessionStorage while signed in").toBeTruthy();

    await page.reload();
    await expect(page.getByRole("heading", { name: "Dashboard" })).toBeVisible();
  });

  /**
   * The access token must never reach a storage a later script or a browser
   * extension could read. This is the invariant the whole session model is
   * built around, and it is cheap to assert where it is actually true — in a
   * real document, against real storage.
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

    // A JWT is three base64url segments separated by dots. The refresh token
    // is deliberately not one — it is an opaque random string — so this
    // finds an access or id token and nothing else.
    const looksLikeAJWT = /^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/;
    for (const [where, entries] of Object.entries(stored)) {
      for (const [key, value] of Object.entries(entries)) {
        expect(
          looksLikeAJWT.test(value),
          `${where}Storage["${key}"] holds what looks like a JWT; the access token must stay in memory`,
        ).toBe(false);
      }
    }

    // localStorage should hold nothing of ours at all.
    expect(Object.keys(stored.local)).toHaveLength(0);
  });
});
