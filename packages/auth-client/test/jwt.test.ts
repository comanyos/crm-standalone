import { describe, expect, it } from "vitest";
import { decodeAccessToken } from "../src/jwt.js";

function fakeJwt(payload: Record<string, unknown>): string {
  const b64url = (obj: unknown) => {
    const bytes = new TextEncoder().encode(JSON.stringify(obj));
    let binary = "";
    for (const b of bytes) binary += String.fromCharCode(b);
    return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  };
  return `${b64url({ alg: "RS256", typ: "JWT" })}.${b64url(payload)}.fake-signature`;
}

describe("decodeAccessToken", () => {
  it("decodes standard and AuthinCore-specific claims", () => {
    const token = fakeJwt({
      sub: "identity-1",
      iss: "https://auth.example.com",
      org_id: "org-1",
      email: "user@example.com",
      roles: ["crm_admin"],
      scopes: ["crm:account:read"],
      exp: 1893456000,
      iat: 1893452400,
    });
    const claims = decodeAccessToken(token);
    expect(claims.sub).toBe("identity-1");
    expect(claims.org_id).toBe("org-1");
    expect(claims.email).toBe("user@example.com");
    expect(claims.roles).toEqual(["crm_admin"]);
    expect(claims.exp).toBe(1893456000);
  });

  it("handles non-ASCII claim values (base64url + UTF-8)", () => {
    const token = fakeJwt({ sub: "id", org_id: "org", email: "üser@example.com", exp: 1, iat: 1 });
    expect(decodeAccessToken(token).email).toBe("üser@example.com");
  });

  it("rejects a malformed token", () => {
    expect(() => decodeAccessToken("not-a-jwt")).toThrow();
  });
});
