import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderRoutes } from "../test/harness";
import AdminPage from "./AdminPage";

const auth = vi.hoisted(() => ({
  hasRole: vi.fn(),
  session: {
    organization: { id: "org-savvy", name: "Savvy CRM" },
    user: { id: "usr-support", email: "support@savvyadvi.com" },
  },
}));

const api = vi.hoisted(() => ({
  listAdminOrganizations: vi.fn(),
  listAdminMembers: vi.fn(),
  listAdminUsers: vi.fn(),
  createAdminOrganization: vi.fn(),
  updateAdminOrganization: vi.fn(),
  createAdminMember: vi.fn(),
  updateAdminMember: vi.fn(),
  listOrganizationJoinRequests: vi.fn(),
  reviewOrganizationJoinRequest: vi.fn(),
  rotateOrganizationJoinCode: vi.fn(),
  setOrganizationJoinCodeEnabled: vi.fn(),
}));

vi.mock("../auth/AuthProvider", () => ({ useAuth: () => auth }));
vi.mock("../lib/AppEnvContext", () => ({ useAppEnv: () => ({ crmApiUrl: "https://api.example.test" }) }));
vi.mock("../auth/admin-api", () => api);

const organization = {
  id: "org-savvy",
  name: "Savvy CRM",
  plan: "pro",
  active: true,
  memberCount: 1,
  joinCodeEnabled: false,
  joinCodeHint: "",
  pendingJoinRequestCount: 0,
  createdAt: "2026-09-11T00:00:00Z",
  updatedAt: "2026-09-11T00:00:00Z",
};

const member = {
  userId: "usr-support",
  organizationId: "org-savvy",
  email: "support@savvyadvi.com",
  firstName: "Savvy",
  lastName: "Support",
  role: "crm_admin",
  active: true,
  superAdmin: false,
  createdAt: "2026-09-11T00:00:00Z",
  updatedAt: "2026-09-11T00:00:00Z",
};

const superAdminUser = {
  userId: "usr-support",
  email: "support@savvyadvi.com",
  firstName: "Savvy",
  lastName: "Support",
  organizationId: "org-savvy",
  organizationName: "Savvy CRM",
  plan: "pro",
  role: "crm_admin",
  active: true,
  superAdmin: true,
  registeredAt: "2026-09-11T00:00:00Z",
};

const customerOrganization = {
  ...organization,
  id: "org-customer",
  name: "Customer workspace",
};

const customerUser = {
  ...superAdminUser,
  userId: "usr-customer",
  email: "owner@example.com",
  firstName: "Customer",
  lastName: "Owner",
  organizationId: "org-customer",
  organizationName: "Customer workspace",
  superAdmin: false,
};

describe("AdminPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Include stale platform rows to prove the UI never treats them as a
    // customer organization or registered CRM user.
    api.listAdminOrganizations.mockResolvedValue([organization, customerOrganization]);
    api.listAdminMembers.mockResolvedValue([member]);
    api.listAdminUsers.mockResolvedValue([superAdminUser, customerUser]);
    api.listOrganizationJoinRequests.mockResolvedValue([]);
  });

  it("shows only customer organizations and users in the platform overview", async () => {
    auth.hasRole.mockImplementation((role: string) => role === "platform_admin");
    renderRoutes([{ path: "/admin", element: <AdminPage /> }], ["/admin"]);

    expect(await screen.findByRole("heading", { name: "Super Admin" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create organization" })).toBeInTheDocument();
    expect(await screen.findByText("owner@example.com")).toBeInTheDocument();
    expect(screen.queryByText("support@savvyadvi.com")).not.toBeInTheDocument();
    expect(screen.queryByText("Super Admin home")).not.toBeInTheDocument();
    expect(screen.queryByText("Savvy CRM")).not.toBeInTheDocument();
    expect(screen.getAllByText("Pro").length).toBeGreaterThan(0);
    expect(screen.getAllByRole("button", { name: "Add user" }).length).toBeGreaterThan(0);
    expect(screen.queryByText("Workspace configuration")).not.toBeInTheDocument();
    expect(api.listAdminUsers).toHaveBeenCalledWith("https://api.example.test");
    expect(api.listAdminMembers).not.toHaveBeenCalled();
  });

  it("keeps a workspace administrator inside their own organization", async () => {
    auth.hasRole.mockImplementation((role: string) => role === "crm_admin");
    api.listAdminOrganizations.mockResolvedValue([organization]);
    renderRoutes([{ path: "/admin", element: <AdminPage /> }], ["/admin"]);

    expect(await screen.findByRole("heading", { name: "Organization administration" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create organization" })).not.toBeInTheDocument();
    expect(await screen.findByText("support@savvyadvi.com")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove user" })).not.toBeInTheDocument();
    expect(api.listAdminOrganizations).toHaveBeenCalledWith("https://api.example.test");
    expect(api.listAdminUsers).not.toHaveBeenCalled();
  });

  it("removes an ordinary user's access only after confirmation", async () => {
    auth.hasRole.mockImplementation((role: string) => role === "platform_admin");
    api.updateAdminMember.mockResolvedValue({ ...customerUser, active: false });
    renderRoutes([{ path: "/admin", element: <AdminPage /> }], ["/admin"]);

    expect(await screen.findByText("owner@example.com")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove user" }));

    expect(screen.getByRole("heading", { name: "Remove user access" })).toBeInTheDocument();
    expect(api.updateAdminMember).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Remove access" }));

    await waitFor(() =>
      expect(api.updateAdminMember).toHaveBeenCalledWith(
        "https://api.example.test",
        "org-customer",
        "usr-customer",
        { active: false },
      ),
    );
  });

  it("does not load or expose admin controls to other members", () => {
    auth.hasRole.mockReturnValue(false);
    renderRoutes([{ path: "/admin", element: <AdminPage /> }], ["/admin"]);

    expect(screen.getByText("Administrator access required")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add user" })).not.toBeInTheDocument();
    expect(api.listAdminOrganizations).not.toHaveBeenCalled();
    expect(api.listAdminUsers).not.toHaveBeenCalled();
  });

  it("lets a workspace administrator approve a verified code request with a server-owned role", async () => {
    auth.hasRole.mockImplementation((role: string) => role === "crm_admin");
    api.listAdminOrganizations.mockResolvedValue([{ ...organization, pendingJoinRequestCount: 1 }]);
    const request = {
      id: "join_1234",
      organizationId: "org-savvy",
      organizationName: "Savvy CRM",
      userId: "usr-applicant",
      email: "applicant@example.com",
      firstName: "New",
      lastName: "Member",
      status: "pending",
      requestedAt: "2026-09-11T00:00:00Z",
    };
    api.listOrganizationJoinRequests.mockResolvedValue([request]);
    api.reviewOrganizationJoinRequest.mockResolvedValue({ ...request, status: "approved", assignedRole: "crm_member" });
    renderRoutes([{ path: "/admin", element: <AdminPage /> }], ["/admin"]);

    expect(await screen.findByText("applicant@example.com")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(screen.getByRole("heading", { name: "Approve organization access" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Approve access" }));

    await waitFor(() =>
      expect(api.reviewOrganizationJoinRequest).toHaveBeenCalledWith(
        "https://api.example.test",
        "org-savvy",
        "join_1234",
        "approve",
        "crm_member",
      ),
    );
  });
});
