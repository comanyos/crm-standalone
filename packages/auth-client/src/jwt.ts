import type { AccessTokenClaims } from "./types.js";

/** Decodes (does NOT verify) a JWT's payload. This is for UI hints only —
 * hiding navigation/actions the caller likely lacks permission for. The
 * backend re-validates the token's signature on every request and remains
 * the sole authorization authority; never make a security decision purely
 * from this. */
export function decodeAccessToken(token: string): AccessTokenClaims {
  const parts = token.split(".");
  if (parts.length !== 3) {
    throw new Error("jwt: not a three-part JWT");
  }
  const payload = parts[1].replace(/-/g, "+").replace(/_/g, "/");
  const padded = payload + "=".repeat((4 - (payload.length % 4)) % 4);
  const json = decodeURIComponent(
    atob(padded)
      .split("")
      .map((c) => "%" + c.charCodeAt(0).toString(16).padStart(2, "0"))
      .join(""),
  );
  return JSON.parse(json) as AccessTokenClaims;
}
