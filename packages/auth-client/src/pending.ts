/** Transient, short-lived PKCE state — never a token, never long-lived.
 * Stored in sessionStorage (cleared when the tab closes) and expired after
 * 10 minutes even within a tab, so a stale entry from an abandoned login
 * attempt can never be replayed. */

export interface PendingRequest {
  codeVerifier: string;
  redirectAfter: string;
  createdAt: number;
}

const PREFIX = "crm_auth_pending:";
const MAX_AGE_MS = 10 * 60 * 1000;

function key(state: string): string {
  return PREFIX + state;
}

export function storePending(state: string, req: Omit<PendingRequest, "createdAt">): void {
  sessionStorage.setItem(key(state), JSON.stringify({ ...req, createdAt: Date.now() }));
}

/** Reads and removes the pending request for `state`. Returns null if it
 * does not exist or has expired — both are treated as "this callback cannot
 * be trusted," never as a soft warning. */
export function consumePending(state: string): PendingRequest | null {
  const k = key(state);
  const raw = sessionStorage.getItem(k);
  sessionStorage.removeItem(k);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as PendingRequest;
    if (Date.now() - parsed.createdAt > MAX_AGE_MS) return null;
    return parsed;
  } catch {
    return null;
  }
}
