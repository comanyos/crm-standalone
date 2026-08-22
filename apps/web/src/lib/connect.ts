import { createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { CRMService, ModuleService } from "@crm/contracts";

/** Attaches the current access token to every outgoing RPC. Reading it via a
 * callback (rather than a value captured once) means a token renewed after
 * the client was created is still picked up on the next call. */
function authInterceptor(getAccessToken: () => string | null): Interceptor {
  return (next) => async (req) => {
    const token = getAccessToken();
    if (token) {
      req.header.set("Authorization", `Bearer ${token}`);
    }
    return next(req);
  };
}

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

export function createCrmClients(baseUrl: string, getAccessToken: () => string | null) {
  const transport = createConnectTransport({
    baseUrl,
    interceptors: [traceContextInterceptor(), authInterceptor(getAccessToken)],
  });
  return {
    crm: createClient(CRMService, transport),
    module: createClient(ModuleService, transport),
  };
}

export type CrmClients = ReturnType<typeof createCrmClients>;
