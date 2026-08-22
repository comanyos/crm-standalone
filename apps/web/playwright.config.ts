import { defineConfig, devices } from "@playwright/test";
import { loadEnv } from "vite";
import { AUTH_STATE_PATH } from "./tests-e2e/helpers";

/**
 * The origin the dev server will actually bind.
 *
 * Derived from the same env vite.config.ts reads, not hardcoded. Hardcoding
 * ":3000" here while vite honours VITE_WEB_PORT meant that on any machine
 * setting a different port — port 3000 is a common collision — `npm run dev`
 * bound one origin while Playwright waited on another, and the suite could
 * not start at all. The redirect URI registered with AuthinCore has to match
 * this too, which is why one value has to drive all three.
 */
const env = loadEnv(process.env.NODE_ENV ?? "development", process.cwd(), "");
const port = env.VITE_WEB_PORT ?? "3000";
// localhost rather than 127.0.0.1 in the URL: the OAuth redirect URI and both
// backends' CORS allowlists are written with the hostname, and the two are
// different origins to a browser.
const appOrigin = `http://localhost:${port}`;
// Handed to the test process so tests-e2e/helpers.ts names the same origin
// without re-deriving it from a different source of truth.
process.env.PLAYWRIGHT_APP_ORIGIN = appOrigin;

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
    baseURL: appOrigin,
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
    url: appOrigin,
    // Always start a fresh instance rather than trusting whatever is
    // already bound to that port — on a machine that also runs an unrelated
    // app on that port (seen locally: a "com.docker.backend"-proxied
    // service), reusing it silently tests the wrong application instead of
    // failing loudly. See apps/web/README.md.
    reuseExistingServer: false,
  },
});
