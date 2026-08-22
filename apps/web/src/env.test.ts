import { describe, expect, it } from "vitest";
import { EnvValidationError, loadEnv } from "./env";

const validDevEnv = {
  VITE_AUTH_ISSUER_URL: "http://localhost:18080",
  VITE_AUTH_ORG_ID: "11111111-1111-1111-1111-111111111111",
  VITE_AUTH_CLIENT_ID: "crm-web",
  VITE_CRM_API_URL: "http://localhost:19431",
};

describe("loadEnv", () => {
  it("accepts a valid development configuration (http allowed)", () => {
    const env = loadEnv(validDevEnv, false);
    expect(env).toEqual({
      authIssuerUrl: "http://localhost:18080",
      authOrgId: validDevEnv.VITE_AUTH_ORG_ID,
      authClientId: "crm-web",
      crmApiUrl: "http://localhost:19431",
    });
  });

  it("defaults VITE_AUTH_CLIENT_ID to crm-web when unset", () => {
    const { VITE_AUTH_CLIENT_ID: _drop, ...rest } = validDevEnv;
    const env = loadEnv(rest, false);
    expect(env.authClientId).toBe("crm-web");
  });

  it("rejects a missing VITE_AUTH_ORG_ID with no default", () => {
    const { VITE_AUTH_ORG_ID: _drop, ...rest } = validDevEnv;
    expect(() => loadEnv(rest, false)).toThrow(EnvValidationError);
  });

  it("rejects a non-URL VITE_CRM_API_URL", () => {
    expect(() => loadEnv({ ...validDevEnv, VITE_CRM_API_URL: "not-a-url" }, false)).toThrow(
      EnvValidationError,
    );
  });

  it("allows http:// URLs outside a production build", () => {
    expect(() => loadEnv(validDevEnv, false)).not.toThrow();
  });

  it("allows loopback http:// URLs in a production build for local Docker Compose", () => {
    expect(() => loadEnv(validDevEnv, true)).not.toThrow();
  });

  it("rejects non-loopback http:// URLs in a production build", () => {
    const insecureProductionEnv = {
      ...validDevEnv,
      VITE_AUTH_ISSUER_URL: "http://auth.example.com",
      VITE_CRM_API_URL: "http://api.example.com",
    };
    expect(() => loadEnv(insecureProductionEnv, true)).toThrow(/https/);
  });

  it("accepts https:// URLs in a production build", () => {
    const prodEnv = {
      ...validDevEnv,
      VITE_AUTH_ISSUER_URL: "https://auth.example.com",
      VITE_CRM_API_URL: "https://api.example.com",
    };
    expect(() => loadEnv(prodEnv, true)).not.toThrow();
  });

  it("collects every validation issue, not just the first", () => {
    try {
      loadEnv({ VITE_AUTH_ISSUER_URL: "bad", VITE_CRM_API_URL: "bad" }, false);
      throw new Error("expected loadEnv to throw");
    } catch (err) {
      expect(err).toBeInstanceOf(EnvValidationError);
      expect((err as EnvValidationError).issues.length).toBeGreaterThan(1);
    }
  });
});
