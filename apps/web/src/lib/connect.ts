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

export function createCrmClients(baseUrl: string, getAccessToken: () => string | null) {
  const transport = createConnectTransport({
    baseUrl,
    interceptors: [authInterceptor(getAccessToken)],
  });
  return {
    crm: createClient(CRMService, transport),
    module: createClient(ModuleService, transport),
  };
}

export type CrmClients = ReturnType<typeof createCrmClients>;
