# Savvy CRM Azure beta deployment

**Status:** Deployed beta environment (September 11, 2026)

**Current immutable release:** `beta-20260911-010`

**Azure subscription:** Microsoft Azure Sponsorship
**Resource group:** `verse`
**Workload region:** Central India
**Marketing edge region:** Azure Static Web Apps, East Asia/global edge

This environment is deliberately isolated from the existing KVerse resources
in `verse`. Every created resource uses a `savvy-crm` name or a globally unique
Savvy CRM prefix.

## Public entry points

| URL | Purpose | Hosting |
|---|---|---|
| `https://crm.savvyadvi.com` | Marketing website and combined Login / Sign-up UI | Azure Static Web Apps Standard |
| `https://crmapp.savvyadvi.com` | Authenticated React CRM application | Caddy → Nginx container on the beta VM |
| `https://crmapi.savvyadvi.com` | CRM API, passwordless auth, Google callback, and lead capture | Caddy → Go API container on the beta VM |
| `https://crmapp.savvyadvi.com/admin` | Organization, plan, and user administration | Workspace members for `crm_admin`; dedicated platform-only console for protected Super Admin `support@savvyadvi.com` |

The browser receives a Secure, HttpOnly session cookie. No Bearer token is put
in a URL or browser storage. Login supports Google and passwordless email OTP;
the Google production callback is
`https://crmapi.savvyadvi.com/auth/google/callback`.

Signup now supports both creating a workspace and requesting access to an
existing organization. A workspace administrator can generate, rotate, or
pause its high-entropy organization code and must approve each verified
applicant with a fixed CRM role. Migration `0039` stores only the code digest
and hint, records pending decisions and audit history, and prevents a pending
applicant from receiving a session or accidentally creating another workspace.

The Super Admin is intentionally excluded from tenant CRM screens and scopes.
Its console lists customer users, customer organizations, current plans,
status, and access controls. Its protected authentication identity and internal
session-anchor organization are not customer records, seats, or plans and are
excluded from those tables and totals. Database migration `0038` prevents
ordinary SQL or application operations from deleting, disabling, moving, or
downgrading the protected support identity and its anchor organization
membership.

Customer user rows include a visible, confirmed **Remove user** action. Removal
revokes the selected organization membership while retaining CRM records and
administration audit history so access can be restored safely. The active
operator and protected Super Admin never receive this action, and the API also
prevents removal of an organization's last active administrator.

## Azure resources

| Resource | Name | Configuration |
|---|---|---|
| Virtual machine | `savvy-crm-beta-vm` | `Standard_B2ms`, 2 vCPU, 8 GiB RAM, Ubuntu 24.04 Trusted Launch |
| Public IP / DNS | `savvy-crm-beta-ip` | Static Standard IP; VM FQDN `savvy-crm-beta-8bc52d.centralindia.cloudapp.azure.com` |
| Network | `savvy-crm-vnet` / `savvy-crm-app-subnet` | Dedicated `10.60.0.0/16` VNet and `10.60.1.0/24` subnet |
| Network security group | `savvy-crm-nsg` | Inbound internet access only on TCP 80 and 443; no public SSH, PostgreSQL, or NATS |
| Data disk | `savvy-crm-data` | 64 GiB Standard SSD; Docker state and databases persist here |
| Container registry | `savvycrm8bc52d` | Basic ACR; VM pull credential is repository-scoped and read-only |
| Key Vault | `savvy-crm-kv-8bc52d` | RBAC, soft delete, purge protection; runtime secrets never enter the repository |
| Backup storage | `stsavvycrm8bc52d` | Standard LRS, HTTPS-only, TLS 1.2 minimum, anonymous and shared-key access disabled |
| Marketing app | `savvy-crm-marketing-8bc52d` | Static Web Apps Standard; default fallback host `nice-bay-05a047b00.3.azurestaticapps.net` |

The VM system identity has only `Key Vault Secrets User` on the Savvy vault
and `Storage Blob Data Contributor` on the Savvy backup account. It does not
receive permissions over other `verse` workloads.

## Runtime topology

```text
Internet
  ├─ crm.savvyadvi.com ── Azure Static Web Apps
  └─ crmapp / crmapi ──── NSG: 80,443 only
                              │
                           Caddy TLS
                         ┌────┴────┐
                      React UI    Go API/auth
                                      │
                         private Docker network
                             ┌────────┴───────┐
                         PostgreSQL 17     NATS JetStream
                             │                 │
                         encrypted Azure managed disk

Nightly pg_dump ── managed identity ── private Azure Blob container
```

PostgreSQL requires TLS 1.2 and the API uses `sslmode=verify-full` against an
internal CA. Migrations rotate the development role passwords to random Key
Vault values before the API starts. PostgreSQL and NATS remain on a Docker
internal network; only the API receives a separate outbound-only bridge for
SMTP, Google, and future provider APIs. PostgreSQL, NATS, the API container
port, and the Nginx container port are not published on the VM host.

## Backups and retention

- A systemd timer runs a PostgreSQL custom-format backup nightly at 02:15 UTC.
- Each dump and SHA-256 checksum is uploaded with the VM managed identity to
  the private `db-backups` container.
- Azure Storage encryption protects blobs at rest; HTTPS protects transfer.
- Local copies older than seven days are removed. Blob lifecycle deletes
  backups after 35 days.
- A backup is not considered a complete recovery plan until a scheduled
  isolated restore drill records the achieved RPO and RTO.

## Capacity and cost boundary

This single-VM beta is sized for roughly **100–200 light beta users**, not 200
simultaneously busy sessions. Actual capacity must be set by load-test results,
database growth, automation volume, and integration traffic.

The planning range is approximately **USD 70–130/month** for the VM, disks,
Static Web Apps Standard, ACR, storage, and modest egress/log usage. It is an
estimate, not a quote; review Azure Cost Management after the first seven days.

This is not the general-availability architecture. The VM, local PostgreSQL,
and local JetStream are single points of failure; there is no automatic
multi-zone failover or point-in-time database recovery. Move to managed,
redundant services before a larger or contractual production launch.

## Integration status

- Enabled for beta: passwordless SMTP login, Google account login, generic
  signed lead-capture endpoint.
- Enabled in `beta-20260911-010`: My Day, new-lead in-app/email alerts,
  untouched-lead SLA escalation, and durable delayed workflow sequences.
- Not enabled by this deployment: Gmail inbox sync and Google Calendar sync
  (they require their own complete provider OAuth credential/key group).
- Deployed but not enabled until external credentials arrive:
  native Meta Business → Page → Form selection, Facebook/Instagram Lead Ads
  webhooks, signature checking, retry/idempotency, history import, attribution,
  token health/reconnect, and Lead-level consent-gated WhatsApp handoff.
- Still external P0 acceptance: Meta app review/production credentials, one
  Facebook and one Instagram provider test lead, and physical iOS/Android
  WhatsApp handoff. Official WhatsApp Business webhooks and Google Ads lead
  forms remain later scope.

The marketing site remains portable to private S3 + CloudFront, but only Azure
Static Web Apps owns the canonical `crm.savvyadvi.com` DNS record in this beta.

## Operator checks

```sh
az vm get-instance-view -g verse -n savvy-crm-beta-vm \
  --query instanceView.statuses -o table

az vm run-command invoke -g verse -n savvy-crm-beta-vm \
  --command-id RunShellScript \
  --scripts 'cd /opt/savvy-crm && docker compose --env-file runtime.env -f compose.yaml ps'

az storage blob list --account-name stsavvycrm8bc52d \
  --container-name db-backups --auth-mode login \
  --query '[].{name:name,size:properties.contentLength}' -o table
```

Do not print `/opt/savvy-crm/runtime.env`, Key Vault secret values, container
environments, or OAuth/SMTP credentials during support work.
