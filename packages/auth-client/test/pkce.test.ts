import { describe, expect, it } from "vitest";
import { codeChallengeS256, randomToken } from "../src/pkce.js";

describe("randomToken", () => {
  it("produces URL-safe strings with no padding", () => {
    const t = randomToken();
    expect(t).toMatch(/^[A-Za-z0-9_-]+$/);
    expect(t.length).toBeGreaterThan(20);
  });

  it("never repeats across calls", () => {
    const seen = new Set(Array.from({ length: 50 }, () => randomToken()));
    expect(seen.size).toBe(50);
  });
});

describe("codeChallengeS256", () => {
  it("matches the RFC 7636 appendix B worked example", async () => {
    // https://www.rfc-editor.org/rfc/rfc7636#appendix-B
    const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk";
    const challenge = await codeChallengeS256(verifier);
    expect(challenge).toBe("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM");
  });

  it("is deterministic for the same verifier", async () => {
    const verifier = randomToken();
    const a = await codeChallengeS256(verifier);
    const b = await codeChallengeS256(verifier);
    expect(a).toBe(b);
  });

  it("differs for different verifiers", async () => {
    const a = await codeChallengeS256("verifier-one-xxxxxxxxxxxxxxxxxxxxxxxxxxxxx");
    const b = await codeChallengeS256("verifier-two-xxxxxxxxxxxxxxxxxxxxxxxxxxxxx");
    expect(a).not.toBe(b);
  });
});
