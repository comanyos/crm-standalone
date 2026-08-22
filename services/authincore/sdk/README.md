# AuthinCore SDKs

Supported service-integration packages:

- `go/authincore`: Go JWT/JWKS validator and `net/http` tenant/scope middleware.
- `node`: dependency-free Node.js validator plus Express-compatible middleware.
- `python`: Python validator using PyJWT's cached JWKS client.

All SDKs enforce the configured issuer and audience, token expiry, RSA signing
algorithm, organization claim, and requested scopes. Unknown signing key IDs
trigger a JWKS refresh so key rotation does not require service restarts.

Consumers should resolve the expected tenant from a trusted host-to-tenant map
or their own persisted resource, never from a caller-controlled request body.
