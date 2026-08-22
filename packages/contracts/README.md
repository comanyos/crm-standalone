# Contracts

Generated TypeScript/Connect-ES client for the CRM and platform-module
contracts defined in `services/crm-api/proto`. This is the only place UI code
should import request/response shapes and service clients from — do not
hand-duplicate them.

## Regenerating

```sh
npm install
npm run generate --workspace=@crm/contracts
```

This runs `buf generate` against `../../services/crm-api/proto` using
`@bufbuild/protoc-gen-es` (v2), which emits both the message types and the
service descriptors (`CRMService`, `ModuleService`) — no separate
`protoc-gen-connect-es` plugin is needed with this generator version. Commit
the regenerated `gen/` output in the same change as the `.proto` edit that
produced it, per the generated-code policy in the root `CONTRIBUTING.md`.

## Usage

```ts
import { createClient } from "@connectrpc/connect";
import { CRMService } from "@crm/contracts";

const client = createClient(CRMService, transport);
const { accounts } = await client.listAccounts({ pageSize: 25 });
```
