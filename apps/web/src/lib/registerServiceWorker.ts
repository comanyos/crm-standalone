/** Registers the app-shell service worker (task_crm.md §10.1).
 *
 * Registration is deliberately best-effort and never blocking. A worker that
 * fails to register — an unsupported browser, a private window, a locked-down
 * enterprise policy, an embedded webview — must leave a fully working online
 * app behind, because the only thing it adds is opening faster and failing
 * more clearly offline.
 *
 * It is skipped in development: a service worker caching a shell in front of
 * Vite's dev server produces exactly the kind of "why am I seeing the old
 * build" confusion that costs an afternoon.
 *
 * Registration happens immediately rather than on `window.load`, which is the
 * more common recipe. The usual argument for waiting is to avoid competing
 * with the initial render for bandwidth — but this app redirects an
 * unauthenticated visitor to the identity provider as soon as React mounts,
 * and a first visit is unauthenticated by definition. Waiting for `load`
 * means racing that redirect: lose, and the very first visit installs
 * nothing. Registering now costs one request that the browser deprioritises
 * anyway.
 */
export function registerServiceWorker(): void {
  if (!import.meta.env.PROD) return;
  if (typeof navigator === "undefined" || !("serviceWorker" in navigator)) return;

  void navigator.serviceWorker.register("/sw.js").catch(() => {
    // Nothing to tell the user: the app works, it just will not open offline.
    // Reporting this would be noise about a capability they did not ask for.
  });
}
