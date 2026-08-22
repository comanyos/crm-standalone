/** PKCE (RFC 7636) and OAuth state/nonce generation. Uses Web Crypto only —
 * no dependency on Node's `crypto` module, so this runs unmodified in every
 * browser and in vitest's jsdom environment. */

function base64url(bytes: Uint8Array): string {
  let binary = "";
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function randomBytes(length: number): Uint8Array {
  const bytes = new Uint8Array(length);
  crypto.getRandomValues(bytes);
  return bytes;
}

/** A URL-safe random string suitable for `state`, `nonce`, or a PKCE verifier. */
export function randomToken(byteLength = 32): string {
  return base64url(randomBytes(byteLength));
}

/** Derives the S256 PKCE code_challenge for a given code_verifier. */
export async function codeChallengeS256(verifier: string): Promise<string> {
  const data = new TextEncoder().encode(verifier);
  const digest = await crypto.subtle.digest("SHA-256", data);
  return base64url(new Uint8Array(digest));
}
