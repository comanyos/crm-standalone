/* Service worker: the app shell only (task_crm.md §10.1).
 *
 * What this deliberately does NOT do is cache or queue any CRM data. Offline
 * mutation of a multi-tenant CRM with optimistic concurrency is a
 * distributed-systems project, not a caching strategy: every write here
 * compares updated_at against the server, so a mutation replayed from a queue
 * an hour later either fails or silently clobbers somebody. And a cached
 * record is a tenant's data sitting in a browser after the session that was
 * allowed to see it has ended.
 *
 * So: the shell is cached so the app opens instantly and can say "you are
 * offline"; every request for data goes to the network and fails honestly
 * when there is none.
 */

const SHELL_CACHE = "crm-shell-v1";

/* Only the entry document. Hashed asset URLs change on every build, so
 * listing them here would mean a stale list; they are cached as they are
 * fetched instead. */
const SHELL_URLS = ["/", "/manifest.webmanifest", "/icon.svg"];

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(SHELL_CACHE)
      .then((cache) => cache.addAll(SHELL_URLS))
      // A shell that fails to pre-cache must not block activation: the app
      // still works online, and refusing to install would leave the previous
      // worker serving an older shell indefinitely.
      .catch(() => undefined)
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(keys.filter((key) => key !== SHELL_CACHE).map((key) => caches.delete(key))),
      )
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;

  /* Anything that is not a plain GET is a write, and writes never come from a
   * cache. */
  if (request.method !== "GET") return;

  const url = new URL(request.url);

  /* Cross-origin is the CRM API and the identity provider. Never cached: one
   * carries tenant data, the other carries an authorization exchange that is
   * single-use by design. */
  if (url.origin !== self.location.origin) return;

  /* Navigations: serve the cached shell when the network is gone, so the app
   * opens and can explain itself rather than showing the browser's error. */
  if (request.mode === "navigate") {
    event.respondWith(
      fetch(request).catch(() => caches.match("/").then((cached) => cached ?? Response.error())),
    );
    return;
  }

  /* Static assets, which are content-hashed and therefore safe to keep:
   * a changed file has a different URL. */
  if (["script", "style", "font", "image"].includes(request.destination)) {
    event.respondWith(
      caches.match(request).then(
        (cached) =>
          cached ??
          fetch(request).then((response) => {
            /* Only complete, same-origin responses. Caching an opaque or
             * partial one means serving a broken asset from then on. */
            if (response.ok && response.type === "basic") {
              const copy = response.clone();
              caches.open(SHELL_CACHE).then((cache) => cache.put(request, copy));
            }
            return response;
          }),
      ),
    );
  }
});
