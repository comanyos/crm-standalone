type UnauthenticatedResponseListener = () => void;

const listeners = new Set<UnauthenticatedResponseListener>();

/** Reports that the CRM server rejected credentials for an API request.
 * Authentication UI owns the resulting transition; transports only report
 * the fact so every request path follows the same session policy. */
export function reportUnauthenticatedResponse(): void {
  for (const listener of listeners) listener();
}

export function watchForUnauthenticatedResponses(listener: UnauthenticatedResponseListener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
