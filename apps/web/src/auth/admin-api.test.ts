import { afterEach, describe, expect, it, vi } from "vitest";
import {
  listAdminUsers,
  reviewOrganizationJoinRequest,
  rotateOrganizationJoinCode,
  updateAdminMember,
  updateAdminOrganization,
} from "./admin-api";

describe("administration API", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("uses credentialed PATCH requests for plan and access changes", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ organization: { id: "org_1", name: "Acme", plan: "pro", active: true } }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ member: { userId: "usr_1", organizationId: "org_1", role: "crm_manager", active: true } }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    await updateAdminOrganization("https://api.example.test", "org_1", { plan: "pro" });
    await updateAdminMember("https://api.example.test", "org_1", "usr_1", { role: "crm_manager" });

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      "https://api.example.test/auth/admin/organizations/org_1",
      expect.objectContaining({ method: "PATCH", credentials: "include", body: JSON.stringify({ plan: "pro" }) }),
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "https://api.example.test/auth/admin/members/usr_1",
      expect.objectContaining({
        method: "PATCH",
        credentials: "include",
        body: JSON.stringify({ organizationId: "org_1", role: "crm_manager" }),
      }),
    );
  });

  it("loads the platform-wide registered-user view without caching", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ users: [{ userId: "usr_1", email: "one@example.test" }] }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(listAdminUsers("https://api.example.test")).resolves.toEqual([
      { userId: "usr_1", email: "one@example.test" },
    ]);
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.example.test/auth/admin/users",
      expect.objectContaining({ credentials: "include", cache: "no-store" }),
    );
  });

  it("keeps code rotation and approval on credentialed administrator endpoints", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ joinCode: { organizationId: "org_1", enabled: true, code: "SAVVY-ABCD-EFGH-JKLM" } }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ request: { id: "join_1", status: "approved" } }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    vi.stubGlobal("fetch", fetchMock);

    await rotateOrganizationJoinCode("https://api.example.test", "org_1");
    await reviewOrganizationJoinRequest(
      "https://api.example.test",
      "org_1",
      "join_1",
      "approve",
      "crm_member",
    );

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      "https://api.example.test/auth/admin/organizations/org_1/join-code",
      expect.objectContaining({ method: "POST", credentials: "include" }),
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "https://api.example.test/auth/admin/join-requests/join_1",
      expect.objectContaining({
        method: "PATCH",
        credentials: "include",
        body: JSON.stringify({ organizationId: "org_1", decision: "approve", role: "crm_member" }),
      }),
    );
  });
});
