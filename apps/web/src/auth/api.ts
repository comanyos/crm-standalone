import { reportUnauthenticatedResponse } from "./unauthenticated-response";

export interface AuthUser {
  id: string;
  email: string;
  firstName?: string;
  lastName?: string;
}

export interface AuthOrganization {
  id: string;
  name: string;
  plan?: "free" | "pro" | "enterprise";
}

export interface AuthMembership {
  role: string;
}

export interface AuthClaims {
  sub: string;
  email: string;
  given_name?: string;
  family_name?: string;
  organization_id?: string;
  organization_name?: string;
  roles: string[];
  scopes: string[];
  scope: string;
}

/** Browser-safe view of a server session. The opaque session token returned
 * by the verification endpoint is intentionally not represented here: web
 * authentication is carried only by the HttpOnly cookie. */
export interface AuthSession {
  claims: AuthClaims;
  user: AuthUser;
  organization?: AuthOrganization;
  membership?: AuthMembership;
  expiresAt: number;
}

interface SessionResponse {
  user: AuthUser;
  organization?: AuthOrganization;
  membership?: AuthMembership;
  roles?: string[];
  scopes?: string[];
  expiresAt?: string | number;
}

interface ChallengeResponse {
  challengeId: string;
  expiresInSeconds?: number;
}

export interface LoginChallenge {
  challengeId: string;
  expiresInSeconds: number;
}

export interface AuthCapabilities {
  google: boolean;
}

export class AuthApiError extends Error {
  constructor(
    message: string,
    public readonly status: number,
    public readonly code?: string,
  ) {
    super(message);
    this.name = "AuthApiError";
  }
}

function endpoint(apiUrl: string, path: string): string {
  return `${apiUrl.replace(/\/$/, "")}${path}`;
}

export function googleLoginUrl(apiUrl: string): string {
  const url = new URL(endpoint(apiUrl, "/auth/google/start"));
  url.searchParams.set("flow", "login");
  return url.toString();
}

async function parseJson(response: Response): Promise<unknown> {
  const contentType = response.headers.get("content-type") ?? "";
  if (!contentType.includes("application/json")) return undefined;
  return response.json().catch(() => undefined);
}

function errorMessage(body: unknown, fallback: string): { message: string; code?: string } {
  if (!body || typeof body !== "object") return { message: fallback };
  const record = body as Record<string, unknown>;
  const message =
    typeof record.error === "string"
      ? record.error
      : typeof record.message === "string"
        ? record.message
        : fallback;
  return { message, code: typeof record.code === "string" ? record.code : undefined };
}

async function request(apiUrl: string, path: string, init?: RequestInit): Promise<Response> {
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
  return response;
}

function assertUser(value: unknown): asserts value is AuthUser {
  if (
    !value ||
    typeof value !== "object" ||
    typeof (value as AuthUser).id !== "string" ||
    typeof (value as AuthUser).email !== "string"
  ) {
    throw new AuthApiError("The server returned an invalid session.", 502, "invalid_session");
  }
}

function expiresAtMillis(value: string | number | undefined): number {
  if (typeof value === "number" && Number.isFinite(value)) {
    // Accept Unix seconds as well as the JavaScript millisecond convention.
    return value < 10_000_000_000 ? value * 1000 : value;
  }
  if (typeof value === "string") {
    const parsed = Date.parse(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  throw new AuthApiError("The server returned an invalid session expiry.", 502, "invalid_session");
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

export function toAuthSession(body: unknown): AuthSession {
  if (!body || typeof body !== "object") {
    throw new AuthApiError("The server returned an invalid session.", 502, "invalid_session");
  }
  const session = body as SessionResponse;
  assertUser(session.user);
  const membershipRole = session.membership?.role;
  const roles = stringArray(session.roles);
  if (roles.length === 0 && typeof membershipRole === "string" && membershipRole) {
    roles.push(membershipRole);
  }
  const scopes = stringArray(session.scopes);

  return {
    user: session.user,
    organization: session.organization,
    membership: session.membership,
    expiresAt: expiresAtMillis(session.expiresAt),
    claims: {
      sub: session.user.id,
      email: session.user.email,
      given_name: session.user.firstName,
      family_name: session.user.lastName,
      organization_id: session.organization?.id,
      organization_name: session.organization?.name,
      roles,
      scopes,
      scope: scopes.join(" "),
    },
  };
}

export async function loadSession(apiUrl: string): Promise<AuthSession | null> {
  const response = await request(apiUrl, "/auth/session", { cache: "no-store" });
  const body = await parseJson(response);
  if (response.status === 401 || response.status === 403) return null;
  if (!response.ok) {
    const error = errorMessage(body, "Unable to check your session.");
    throw new AuthApiError(error.message, response.status, error.code);
  }
  return toAuthSession(body);
}

/** Capability discovery is intentionally fail-closed. Email OTP remains
 * usable when this optional endpoint is unavailable, while a broken Google
 * integration is never advertised to the user. */
export async function loadAuthCapabilities(apiUrl: string): Promise<AuthCapabilities> {
  try {
    const response = await request(apiUrl, "/auth/capabilities", { cache: "no-store" });
    if (!response.ok) return { google: false };
    const body = await parseJson(response);
    if (!body || typeof body !== "object") return { google: false };
    return { google: (body as { google?: unknown }).google === true };
  } catch {
    return { google: false };
  }
}

export async function requestLoginCode(apiUrl: string, email: string): Promise<LoginChallenge> {
  const response = await request(apiUrl, "/auth/email/request", {
    method: "POST",
    body: JSON.stringify({ email, flow: "login" }),
  });
  const body = await parseJson(response);
  if (!response.ok) {
    const error = errorMessage(body, "We couldn't send a login code. Please try again.");
    throw new AuthApiError(error.message, response.status, error.code);
  }
  const challenge = body as Partial<ChallengeResponse> | undefined;
  if (!challenge || typeof challenge.challengeId !== "string" || !challenge.challengeId) {
    throw new AuthApiError("The server did not create a valid login challenge.", 502, "invalid_challenge");
  }
  return {
    challengeId: challenge.challengeId,
    expiresInSeconds:
      typeof challenge.expiresInSeconds === "number" && challenge.expiresInSeconds > 0
        ? challenge.expiresInSeconds
        : 600,
  };
}

export async function verifyLoginCode(
  apiUrl: string,
  challengeId: string,
  code: string,
): Promise<AuthSession> {
  const response = await request(apiUrl, "/auth/email/verify", {
    method: "POST",
    body: JSON.stringify({ challengeId, code }),
  });
  const body = await parseJson(response);
  if (!response.ok) {
    const error = errorMessage(body, "That code is invalid or has expired.");
    throw new AuthApiError(error.message, response.status, error.code);
  }
  return toAuthSession(body);
}

export async function endSession(apiUrl: string): Promise<void> {
  const response = await request(apiUrl, "/auth/logout", { method: "POST", keepalive: true });
  // Logout is idempotent from the browser's perspective. A missing/expired
  // server session already satisfies the request.
  if (response.ok || response.status === 401 || response.status === 403) return;
  const body = await parseJson(response);
  const error = errorMessage(body, "Unable to finish signing out.");
  throw new AuthApiError(error.message, response.status, error.code);
}
