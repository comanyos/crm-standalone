import { AuthApiError } from "./api";
import { reportUnauthenticatedResponse } from "./unauthenticated-response";

export type OrganizationPlan = "free" | "pro" | "enterprise";
export type CrmRole = "crm_admin" | "crm_manager" | "crm_member" | "crm_viewer";

export interface AdminOrganization {
  id: string;
  name: string;
  plan: OrganizationPlan;
  active: boolean;
  memberCount: number;
  joinCodeEnabled: boolean;
  joinCodeHint: string;
  joinCodeUpdatedAt?: string;
  pendingJoinRequestCount: number;
  createdAt: string;
  updatedAt: string;
}

export interface OrganizationJoinCode {
  organizationId: string;
  enabled: boolean;
  hint: string;
  updatedAt?: string;
  /** Present only in the response that generates or rotates a code. */
  code?: string;
}

export interface OrganizationJoinRequest {
  id: string;
  organizationId: string;
  organizationName: string;
  userId: string;
  email: string;
  firstName: string;
  lastName: string;
  status: "pending" | "approved" | "rejected";
  assignedRole?: CrmRole;
  requestedAt: string;
  reviewedAt?: string;
}

export interface AdminMember {
  userId: string;
  organizationId: string;
  email: string;
  firstName: string;
  lastName: string;
  role: CrmRole;
  active: boolean;
  superAdmin: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface AdminUser {
  userId: string;
  email: string;
  firstName: string;
  lastName: string;
  organizationId: string;
  organizationName: string;
  plan: OrganizationPlan | "unassigned";
  role: CrmRole | "";
  active: boolean;
  superAdmin: boolean;
  registeredAt: string;
}

export interface CreateOrganizationRequest {
  name: string;
  plan: OrganizationPlan;
  adminEmail: string;
  adminFirstName: string;
  adminLastName: string;
}

export interface CreateMemberRequest {
  organizationId: string;
  email: string;
  firstName: string;
  lastName: string;
  role: CrmRole;
}

function endpoint(apiUrl: string, path: string): string {
  return `${apiUrl.replace(/\/$/, "")}${path}`;
}

async function adminRequest<T>(apiUrl: string, path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(endpoint(apiUrl, path), {
    ...init,
    credentials: "include",
    headers: {
      Accept: "application/json",
      ...(init?.body ? { "Content-Type": "application/json" } : {}),
      ...init?.headers,
    },
  });
  if (response.status === 401) reportUnauthenticatedResponse();
  const body = await response.json().catch(() => undefined);
  if (!response.ok) {
    const record = body && typeof body === "object" ? (body as Record<string, unknown>) : {};
    throw new AuthApiError(
      typeof record.error === "string" ? record.error : "The administration request failed.",
      response.status,
      typeof record.code === "string" ? record.code : undefined,
    );
  }
  return body as T;
}

export async function listAdminOrganizations(apiUrl: string): Promise<AdminOrganization[]> {
  const result = await adminRequest<{ organizations?: AdminOrganization[] }>(
    apiUrl,
    "/auth/admin/organizations",
    { cache: "no-store" },
  );
  return Array.isArray(result.organizations) ? result.organizations : [];
}

export async function listAdminUsers(apiUrl: string): Promise<AdminUser[]> {
  const result = await adminRequest<{ users?: AdminUser[] }>(apiUrl, "/auth/admin/users", {
    cache: "no-store",
  });
  return Array.isArray(result.users) ? result.users : [];
}

export async function createAdminOrganization(
  apiUrl: string,
  input: CreateOrganizationRequest,
): Promise<AdminOrganization> {
  const result = await adminRequest<{ organization: AdminOrganization }>(
    apiUrl,
    "/auth/admin/organizations",
    { method: "POST", body: JSON.stringify(input) },
  );
  return result.organization;
}

export async function updateAdminOrganization(
  apiUrl: string,
  organizationId: string,
  input: Partial<Pick<AdminOrganization, "name" | "plan" | "active">>,
): Promise<AdminOrganization> {
  const result = await adminRequest<{ organization: AdminOrganization }>(
    apiUrl,
    `/auth/admin/organizations/${encodeURIComponent(organizationId)}`,
    { method: "PATCH", body: JSON.stringify(input) },
  );
  return result.organization;
}

export async function listAdminMembers(apiUrl: string, organizationId: string): Promise<AdminMember[]> {
  const query = new URLSearchParams({ organizationId });
  const result = await adminRequest<{ members?: AdminMember[] }>(
    apiUrl,
    `/auth/admin/members?${query.toString()}`,
    { cache: "no-store" },
  );
  return Array.isArray(result.members) ? result.members : [];
}

export async function createAdminMember(apiUrl: string, input: CreateMemberRequest): Promise<AdminMember> {
  const result = await adminRequest<{ member: AdminMember }>(apiUrl, "/auth/admin/members", {
    method: "POST",
    body: JSON.stringify(input),
  });
  return result.member;
}

export async function updateAdminMember(
  apiUrl: string,
  organizationId: string,
  userId: string,
  input: Partial<Pick<AdminMember, "role" | "active">>,
): Promise<AdminMember> {
  const result = await adminRequest<{ member: AdminMember }>(
    apiUrl,
    `/auth/admin/members/${encodeURIComponent(userId)}`,
    {
      method: "PATCH",
      body: JSON.stringify({ organizationId, ...input }),
    },
  );
  return result.member;
}

export async function listOrganizationJoinRequests(
  apiUrl: string,
  organizationId: string,
): Promise<OrganizationJoinRequest[]> {
  const query = new URLSearchParams({ organizationId });
  const result = await adminRequest<{ requests?: OrganizationJoinRequest[] }>(
    apiUrl,
    `/auth/admin/join-requests?${query.toString()}`,
    { cache: "no-store" },
  );
  return Array.isArray(result.requests) ? result.requests : [];
}

export async function reviewOrganizationJoinRequest(
  apiUrl: string,
  organizationId: string,
  requestId: string,
  decision: "approve" | "reject",
  role?: CrmRole,
): Promise<OrganizationJoinRequest> {
  const result = await adminRequest<{ request: OrganizationJoinRequest }>(
    apiUrl,
    `/auth/admin/join-requests/${encodeURIComponent(requestId)}`,
    {
      method: "PATCH",
      body: JSON.stringify({ organizationId, decision, ...(decision === "approve" ? { role } : {}) }),
    },
  );
  return result.request;
}

export async function rotateOrganizationJoinCode(
  apiUrl: string,
  organizationId: string,
): Promise<OrganizationJoinCode> {
  const result = await adminRequest<{ joinCode: OrganizationJoinCode }>(
    apiUrl,
    `/auth/admin/organizations/${encodeURIComponent(organizationId)}/join-code`,
    { method: "POST" },
  );
  return result.joinCode;
}

export async function setOrganizationJoinCodeEnabled(
  apiUrl: string,
  organizationId: string,
  enabled: boolean,
): Promise<OrganizationJoinCode> {
  const result = await adminRequest<{ joinCode: OrganizationJoinCode }>(
    apiUrl,
    `/auth/admin/organizations/${encodeURIComponent(organizationId)}/join-code`,
    { method: "PATCH", body: JSON.stringify({ enabled }) },
  );
  return result.joinCode;
}
