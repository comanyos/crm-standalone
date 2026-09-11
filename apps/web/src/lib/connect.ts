import { Code, ConnectError, createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { CRMService, ModuleService } from "@crm/contracts";
import { reportUnauthenticatedResponse } from "../auth/unauthenticated-response";

/** Starts a W3C trace for each browser-to-CRM RPC. The value is opaque random
 * telemetry context only: it contains no user, tenant, token, URL, or request
 * body data. CRM continues it when OTLP export is enabled. */
export function traceContextInterceptor(): Interceptor {
  return (next) => async (req) => {
    if (!req.header.has("traceparent")) {
      const traceparent = createTraceParent();
      if (traceparent) req.header.set("traceparent", traceparent);
    }
    return next(req);
  };
}

/** Connect errors can arrive in a successful HTTP envelope, so checking the
 * decoded RPC code is required in addition to checking the fetch status. */
export function unauthenticatedResponseInterceptor(): Interceptor {
  return (next) => async (req) => {
    try {
      return await next(req);
    } catch (error) {
      if (ConnectError.from(error).code === Code.Unauthenticated) {
        reportUnauthenticatedResponse();
      }
      throw error;
    }
  };
}

export function createTraceParent(): string | undefined {
  if (!globalThis.crypto?.getRandomValues) return undefined;
  const traceID = new Uint8Array(16);
  const spanID = new Uint8Array(8);
  globalThis.crypto.getRandomValues(traceID);
  globalThis.crypto.getRandomValues(spanID);
  return `00-${hex(traceID)}-${hex(spanID)}-01`;
}

function hex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

export async function credentialedFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const response = await fetch(input, { ...init, credentials: "include" });
  if (response.status === 401) reportUnauthenticatedResponse();
  return response;
}

export function createCrmClients(baseUrl: string) {
  const transport = createConnectTransport({
    baseUrl,
    interceptors: [traceContextInterceptor(), unauthenticatedResponseInterceptor()],
    // The server session is an HttpOnly cookie. Wrapping fetch is the
    // supported Connect-Web hook for including it on cross-origin calls.
    fetch: credentialedFetch,
  });
  return {
    crm: createClient(CRMService, transport),
    module: createClient(ModuleService, transport),
  };
}

export type CrmClients = ReturnType<typeof createCrmClients>;
