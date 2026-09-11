import { describe, expect, it } from "vitest";
import { EnvValidationError, loadEnv } from "./env";

const validDevEnv = {
  VITE_CRM_API_URL: "http://localhost:19431",
  VITE_CRM_AUTH_PORTAL_URL: "http://localhost:4173/?auth=login#top",
};

describe("loadEnv", () => {
  it("accepts the API and marketing auth dialog as browser configuration", () => {
    expect(loadEnv(validDevEnv, false)).toEqual({
      crmApiUrl: "http://localhost:19431",
      authPortalUrl: "http://localhost:4173/?auth=login#top",
    });
  });

  it("does not require legacy issuer, organization, or OAuth client values", () => {
    expect(() => loadEnv(validDevEnv, false)).not.toThrow();
  });

  it("rejects a missing or invalid CRM API URL", () => {
    expect(() => loadEnv({}, false)).toThrow(EnvValidationError);
    expect(() => loadEnv({ ...validDevEnv, VITE_CRM_API_URL: "not-a-url" }, false)).toThrow(EnvValidationError);
    expect(() => loadEnv({ ...validDevEnv, VITE_CRM_AUTH_PORTAL_URL: "not-a-url" }, false)).toThrow(
      EnvValidationError,
    );
  });

  it("allows loopback HTTP in a production build for local Docker Compose", () => {
    expect(() => loadEnv(validDevEnv, true)).not.toThrow();
  });

  it("rejects non-loopback HTTP in a production build", () => {
    expect(() => loadEnv({ ...validDevEnv, VITE_CRM_API_URL: "http://api.example.com" }, true)).toThrow(/https/);
    expect(() =>
      loadEnv({ ...validDevEnv, VITE_CRM_AUTH_PORTAL_URL: "http://crm.example.com" }, true),
    ).toThrow(/https/);
  });

  it("accepts HTTPS in a production build", () => {
    expect(() =>
      loadEnv(
        {
          VITE_CRM_API_URL: "https://crmapi.savvyadvi.com",
          VITE_CRM_AUTH_PORTAL_URL: "https://crm.savvyadvi.com/?auth=login#top",
        },
        true,
      ),
    ).not.toThrow();
  });
});
