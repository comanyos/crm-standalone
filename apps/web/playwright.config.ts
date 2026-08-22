import { defineConfig, devices } from "@playwright/test";
import { AUTH_STATE_PATH } from "./tests-e2e/helpers";

/**
 * Browser tests against a live backend. They assume the Compose stack is
 * already up (`make up`) and bootstrapped (`make bootstrap`), with
 * apps/web/.env pointed at that org — see apps/web/README.md. This config
 * does not start the backend itself; it only starts the web dev server.
 */
export default defineConfig({
  testDir: "./tests-e2e",
  // Parallel, and verified stable that way across repeated runs.
  //
  // This suite used to be pinned to one worker because it failed
  // intermittently in parallel — most visibly the duplicate-website account
  // coming back unflagged. That was never contention between tests: the
  // specs asserted a save had landed with `getByRole("heading", { name })`,
  // which Playwright matches as a substring, so it resolved against the
  // form page's own `Edit <name>` header while the write was still in
  // flight. The next page.goto then cancelled that request, and slower
  // parallel runs simply lost the race more often. Every such assertion now
  // waits for the detail page (tests-e2e/helpers.ts, expectOnDetailPage),
  // and deleteFromDetailPage waits for the delete to land before returning.
  //
  // Password sign-in is still deliberately rare — auth.setup.ts signs in
  // once and every other spec reuses that state — so parallel workers do
  // not contend over AuthinCore's per-IP login rate limit.
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: "html",
  use: {
    baseURL: "http://localhost:3000",
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },
  projects: [
    // Signs in once and saves the session every other spec reuses; see
    // tests-e2e/auth.setup.ts for why re-logging-in per test doesn't work.
    { name: "setup", testMatch: /auth\.setup\.ts/ },
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"], storageState: AUTH_STATE_PATH },
      dependencies: ["setup"],
    },
  ],
  webServer: {
    command: "npm run dev",
    url: "http://localhost:3000",
    // Always start a fresh instance rather than trusting whatever is
    // already bound to :3000 — on a machine that also runs an unrelated
    // app on that port (seen locally: a "com.docker.backend"-proxied
    // service), reusing it silently tests the wrong application instead of
    // failing loudly. See apps/web/README.md.
    reuseExistingServer: false,
  },
});
