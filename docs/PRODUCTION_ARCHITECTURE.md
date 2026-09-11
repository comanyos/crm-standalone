# Savvy CRM production architecture

**Status:** Proposed general-availability baseline; decisions marked **Gate**
must be closed before a larger or contractual launch. The current single-VM
Azure beta is recorded separately in
[`AZURE_BETA_DEPLOYMENT.md`](AZURE_BETA_DEPLOYMENT.md).

**Audience:** Engineering, security, operations, and product owners.

**Scope:** AWS production hosting for the Savvy CRM application, native CRM API/authentication, workers, data stores, provider webhooks, and mobile clients. The marketing artifact also supports Azure Static Web Apps as an alternative host.

The selected public entry points are:

- `crm.savvyadvi.com` — marketing and the combined Login / Sign-up dialog.
- `crmapp.savvyadvi.com` — authenticated web application.
- `crmapi.savvyadvi.com` — CRM API, auth, provider callbacks, and webhooks.

Local Docker exposes the web container at `http://localhost:3003`. This is not
a production listener contract: `https://crmapp.savvyadvi.com` is served on
standard HTTPS port 443, with no customer-visible `:3003` suffix.

Infrastructure placeholders remain for:

- `<region>` — primary workload Region, selected for residency, latency, service availability, and price.
- `<dr-region>` — recovery Region, if cross-Region recovery is approved.
- `<account-id>` — environment AWS account.
- `<env>` — `dev`, `staging`, or `prod`.

Confirm control of the `savvyadvi.com` zone before creating DNS records, certificates, OAuth registrations, mobile association files, or webhook registrations.

## 1. Recommended baseline

Use separate AWS accounts for production and non-production under AWS Organizations. In production:

1. Serve the public marketing site from a private S3 bucket through its own CloudFront distribution and Origin Access Control (OAC). Azure Static Web Apps is a supported manual alternative, not a simultaneous second owner of the canonical hostname.
2. Serve the authenticated Vite/React application from a second private S3 bucket and CloudFront distribution. Do not couple application releases to marketing releases.
3. Route `crmapi.savvyadvi.com` to one internet-facing Application Load Balancer (ALB). The CRM API owns both CRM RPCs and the small `/auth/*` surface; there is no separate identity-service host.
4. Run HTTP services and independently scalable background workers as ECS services on Fargate in private subnets. Only the ALB is internet-facing.
5. Keep the native identity tables (`auth_users`, `auth_organizations`, `auth_memberships`, email challenges, external identities, OAuth state, and sessions) in the CRM PostgreSQL database. Session and OTP tokens are stored only as cryptographic digests. Tenant-owned CRM tables remain protected by RLS.
6. Do not introduce a cache merely for sessions: PostgreSQL is the source of truth. Add a shared Redis/Valkey limiter only when horizontal traffic makes process-local edge controls insufficient, and never store the only durable copy of identity state there.
7. Replace the production event transport with an AWS-native SNS/SQS design behind a transport interface before general availability, or separately approve and operate a genuinely durable NATS JetStream cluster. Amazon MQ is not a managed NATS service.
8. Store uploads, generated exports, and provider artifacts in a private S3 content bucket with tenant-scoped immutable keys, short-lived presigned URLs, malware quarantine, lifecycle rules, and an application metadata record.
9. Store secrets in Secrets Manager, encrypt data using KMS keys, and grant each ECS task role only the secrets and data paths it needs.
10. Build once in GitHub Actions, authenticate to AWS using OIDC, push immutable image digests to ECR, promote the same digest through environments, run schema migrations as controlled one-off ECS tasks, and deploy with automatic service rollback signals.

### Production gates found in the current repository

- **Gate — durable event delivery:** The CRM outbox can accumulate when `NATS_URL` is absent, and the current connection path can fall back to core NATS when JetStream is unavailable. Production must fail startup/readiness when its selected durable transport is unavailable; it must never silently downgrade to non-durable delivery.
- **Gate — shared rate limiting:** Public lead capture and email-OTP issuance need horizontally consistent abuse controls. Before running multiple API tasks, enforce edge rules in AWS WAF and/or a shared Redis-backed limiter, including per-address and per-IP limits.
- **Gate — native-auth hardening:** Prove OTP expiry, single use, attempt/resend limits, account-enumeration behavior, session expiry/revocation, cookie/Origin handling, Google state/nonce/PKCE checks, membership isolation, bootstrap safety, and secret rotation in a production-shaped environment.
- **Gate — production operations:** Current health, metrics, logging, tracing, and local backup utilities are useful foundations, but SLOs, alert delivery, managed backup policies, restore drills, log retention, dashboards, and on-call ownership still need production configuration.

## 2. Logical topology

```text
Internet
   |
Route 53 public hosted zone + ACM certificates
   |
   +-- crm.savvyadvi.com -------- CloudFront + optional WAF
   |                              |
   |                         OAC -> private marketing S3 bucket
   |
   +-- crmapp.savvyadvi.com ----- CloudFront + optional WAF
   |                              |
   |                         OAC -> private application S3 bucket
   |
   +-- crmapi.savvyadvi.com ------- AWS WAF -> public ALB (2+ public AZs)
                                             |
                                      crm-api target group
                                             |
                                  private ECS/Fargate subnets
                                   |                  |
                               workers             RDS PG
                                   |
                         SNS/SQS queues + DLQs (target)
                                   |
                           private S3 content bucket

Management plane: GitHub Actions OIDC -> ECR / IaC / ECS deployments
Operations plane: CloudWatch Logs/Metrics/Alarms, ADOT/OpenTelemetry,
CloudTrail, ALB/WAF logs, AWS Backup, Secrets Manager, KMS
```

No database, optional cache, worker, metrics endpoint, or migration task receives a public route. Administration lives at `crmapp.savvyadvi.com/admin` and requires the server-issued `crm_admin` role. There is no shared admin password and no separate identity admin UI.

## 3. DNS, TLS, and edge behavior

| Name | Purpose | Target | Certificate | Notes |
|---|---|---|---|---|
| `crm.savvyadvi.com` | Marketing website + Login / Sign-up dialog | Route 53 alias to marketing CloudFront (or explicit Azure cut-over) | ACM in `us-east-1` for CloudFront; managed TLS on Azure | Private S3 origin through OAC for the primary AWS design |
| `crmapp.savvyadvi.com` | Authenticated SPA | Route 53 alias to application CloudFront | ACM certificate in `us-east-1` | App `/login` is compatibility-only and redirects to the marketing dialog |
| `crmapi.savvyadvi.com` | CRM API, `/auth/*`, lead capture, provider callbacks | Route 53 alias to ALB | ACM certificate in `<region>` | Host rule to CRM target group; WAF in front |

CloudFront requires the viewer certificate in `us-east-1`; the ALB certificate belongs in the workload Region. Use DNS validation and renew through ACM. Redirect HTTP to HTTPS at the ALB and enforce HTTPS at CloudFront.

### Authentication contract

- The marketing-site Login / Sign-up dialog owns both account flows. Production actions call the CRM API's `/auth/*` routes; no OTP secret, Google client secret, verifier, or server session processing belongs in the static JavaScript.
- Keep two exact CORS allowlists: the authenticated application origin may call the CRM API, while the marketing origin may call only capabilities, Google start, and email request/verify. Never grant the public website credentialed access to CRM RPCs, session inspection, or logout.
- Support two public account paths: Google OAuth/OIDC and passwordless six-digit email OTP. Do not offer or store an application password for this flow.
- Provision the Google account-login client separately from Gmail/Calendar integration clients and scopes. The CRM API is the confidential Google client and validates exact redirect URIs, one-time hashed state, nonce, server-held PKCE verifier, verified email, account linking, logout, revocation, and cross-tenant isolation.
- Store only an OTP verifier/hash with purpose, audience, expiry, attempt count, and consumed timestamp. Enforce short expiry, single use, resend cooldown, per-email/IP/device rate limits, and generic responses that do not reveal account existence.
- After verified signup, create the user, organization, first membership, and onboarding profile transactionally. Capture name, organization, role, sales-team size, industry, primary goal, and optional lead sources without trusting client-provided authorization roles.
- Generate a fresh high-entropy opaque session after authentication and organization creation, and retain only its digest in PostgreSQL. Web receives a Secure, HttpOnly, host-only, appropriately scoped SameSite cookie and no browser-readable token; never carry a Bearer token in the redirect to `crmapp.savvyadvi.com`. State-changing requests require strict allowed-Origin handling. A dedicated native issuance/exchange flow (still a launch gate) returns the opaque token exactly once for Keychain/Keystore storage, after which mobile sends it as `Authorization: Bearer`. Logout revokes the same server session in either transport.

For both static buckets:

- Enable S3 Block Public Access at account and bucket level and keep Object Ownership as **Bucket owner enforced**.
- Use a regular S3 REST origin, CloudFront OAC with **always sign**, and a bucket policy scoped to the exact distribution ARN. OAC does not work with an S3 website endpoint.
- Use independent buckets/distributions/deployment roles for marketing and the application.
- Version hashed assets and send `Cache-Control: public,max-age=31536000,immutable` for them. Give `index.html`, manifests, association files, and service-worker files short or no-cache policies.
- Prefer an extensionless-route rewrite to `/index.html` for the SPA. If custom 403/404 responses are used instead, scope and test them so a missing JavaScript or image asset remains a real 404 rather than returning HTML.
- Invalidate only small mutable entry points such as `/index.html`; hashed assets should not need invalidation.
- Add strict response headers, a tested Content Security Policy, HSTS, MIME sniffing protection, and an explicit referrer policy. Keep the auth cookie host-only on `crmapi.savvyadvi.com` rather than setting it for `.savvyadvi.com`.

AWS references: [restricting S3 origins with OAC](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/private-content-restricting-access-to-s3.html), [secure static-site reference](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/getting-started-secure-static-website-cloudformation-template.html), [S3 Block Public Access](https://docs.aws.amazon.com/AmazonS3/latest/userguide/access-control-block-public-access.html), [S3 Object Ownership](https://docs.aws.amazon.com/AmazonS3/latest/userguide/about-object-ownership.html), [CloudFront certificate requirements](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/cnames-and-https-requirements.html), [Route 53 alias to CloudFront](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/routing-to-cloudfront-distribution.html), [Route 53 alias to an ELB load balancer](https://docs.aws.amazon.com/Route53/latest/DeveloperGuide/routing-to-elb-load-balancer.html), [CloudFront expiration](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/Expiration.html), and [CloudFront invalidation](https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/Invalidation.html).

## 4. VPC and security boundaries

Build one VPC per environment, with at least two Availability Zones in production:

- **Public subnets:** ALB and NAT gateways only. Do not assign public IPs to application tasks.
- **Private application subnets:** ECS/Fargate API and worker tasks.
- **Isolated data subnets:** RDS and any optional shared limiter/cache. If a self-managed JetStream exception is approved, its nodes also belong in private/isolated subnets with persistent volumes.
- **Outbound access:** use one NAT gateway per AZ for the production HA baseline. A single NAT is acceptable only for non-production or a formally accepted pilot risk. Compare interface endpoint hourly charges with NAT processing/egress before adding endpoints.
- **VPC endpoints:** evaluate S3 gateway plus `ecr.api`, `ecr.dkr`, CloudWatch Logs, Secrets Manager, and KMS interface endpoints. Fargate platform 1.4+ pulling private ECR images needs the ECR endpoints plus S3 access when internet egress is removed.

Security groups should reference other security groups, not broad CIDRs:

| Security group | Inbound | Outbound |
|---|---|---|
| ALB | TCP 443 from internet; TCP 80 only for redirect | CRM API target port |
| CRM API | Application port from ALB SG only | CRM database, selected queue endpoints, S3, SMTP and provider APIs |
| CRM workers | None | CRM database, queues, S3, provider APIs |
| CRM RDS | TCP 5432 from CRM API/worker/migration SGs only | Default response traffic |
| Optional limiter cache | TLS port from CRM API SG only | Default response traffic |

Use `awsvpc` networking and target type `ip`. Give every ECS service its own target group. Keep database credentials, queue permissions, and S3 prefixes separated by task role. Use Session Manager or another approved audited access path for emergency operations rather than a standing public bastion or public database rule.

AWS references: [ECS outbound networking](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/networking-outbound.html), [ECS network security](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/security-network.html), [NAT gateway basics](https://docs.aws.amazon.com/vpc/latest/userguide/nat-gateway-basics.html), [VPC endpoints/PrivateLink](https://docs.aws.amazon.com/vpc/latest/privatelink/what-is-privatelink.html), [ECR VPC endpoints](https://docs.aws.amazon.com/AmazonECR/latest/userguide/vpc-endpoints.html), [security-group rules](https://docs.aws.amazon.com/vpc/latest/userguide/security-group-rules.html), and [Session Manager](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager.html).

## 5. ECS services and workers

### Service layout

| ECS service/task | Exposure | Launch count | Scaling signal | Required change or constraint |
|---|---|---:|---|---|
| `crm-api` | ALB target | 2 across AZs | CPU, memory, ALB request count/latency | Owns CRM RPCs and native auth routes; move embedded long-running loops to worker mode; keep `/metrics` private |
| `crm-worker` | No inbound | 1 initially, then per workload | Outbox age, queue depth/oldest message, workflow lag | Separate Gmail, Calendar, workflow, and outbox concurrency or modes as load warrants |
| `db-migrate` | One-off task | On release only | Pipeline result | Dedicated role/secret; never a continuously running service |

The `crm-api` target group uses `/readyz` for ALB health and deployments; `/healthz` is process liveness. Readiness must fail when PostgreSQL or another dependency required for safe request handling is unavailable. A worker should expose health internally or emit a heartbeat/lag metric; it does not need an ALB.

Use Fargate task IAM roles per service. Do not share a broad role across API, workers, and migrations. Pin an explicit Fargate platform version after testing, set CPU/memory reservations at the task level, configure a non-root container user and read-only root filesystem where compatible, and send stdout/stderr to CloudWatch Logs. Use deployment circuit-breaker rollback and CloudWatch alarm rollback, with an appropriate health-check grace period. Rollback protects the ECS service; it does not reverse a database migration.

Keep a minimum of two healthy API tasks across AZs after native-auth horizontal tests pass. Enable Availability Zone rebalancing and target-tracking scaling. Cap maximum counts using downstream database connection budgets rather than an arbitrary large value.

AWS references: [ALB with ECS](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/alb.html), [ECS service load balancing](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/service-load-balancing.html), [container health checks](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/healthcheck.html), [deployment failure detection and rollback](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/deployment-failure-detection.html), [Fargate task parameters](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task_definition_parameters.html), [Fargate tasks and services](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/fargate-tasks-services.html), [task IAM roles](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task-iam-roles.html), [Availability Zone rebalancing](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/service-rebalancing.html), and [target-tracking service scaling](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/service-autoscaling-targettracking.html).

## 6. PostgreSQL

### Production baseline

- Provision one encrypted RDS for PostgreSQL Multi-AZ DB instance for CRM data and the compact native-auth schema, in private DB subnets spanning at least two AZs.
- Use separate database owners, runtime roles, read-only operations roles, and migration roles. The runtime role must not own the schema or gain destructive migration privileges.
- Require TLS with `sslmode=verify-full`, the current RDS CA bundle, and `rds.force_ssl=1` where supported and tested.
- Encrypt the DB, logs, snapshots, backups, and replicas using the approved customer-managed KMS key. The encryption key choice cannot be changed in place after creation.
- Enable storage autoscaling with deliberate minimum/maximum thresholds and alarms well before the maximum.
- Set application pool limits per task, calculate the worst-case total at maximum ECS scale, and reserve connections for migrations/operations.
- Use CloudWatch Database Insights for production visibility. Do not design a new dependency on the legacy Performance Insights console, whose end-of-life AWS documents for July 31, 2026.

A Multi-AZ **DB instance** provides a synchronous standby for failover; its standby is not a read endpoint. A Multi-AZ **DB cluster** has readable standbys but different cost, behavior, and cross-Region backup support. The launch recommendation is Multi-AZ DB instances unless load testing proves that a cluster is needed. This also preserves the documented option to replicate automated backups cross-Region for RDS PostgreSQL DB instances.

RDS Proxy is optional, not part of the initial baseline. The Go services already pool connections; add RDS Proxy only when connection pressure, failover testing, or burst scaling shows a measurable need.

The auth tables are global service metadata while CRM records remain tenant-owned. Keep authorization in the CRM API, derive the tenant only from an active membership tied to the validated session, and continue setting RLS context transactionally before accessing tenant tables.

AWS references: [RDS Multi-AZ deployments](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Concepts.MultiAZ.html), [RDS encryption](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Overview.Encryption.html), [PostgreSQL TLS](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/PostgreSQL.Concepts.General.SSL.html), [storage autoscaling](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_PIOPS.Autoscaling.html), [RDS Proxy](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/rds-proxy.html), [Database Insights/Performance Insights transition](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_PerfInsights.Enabling.html), and [cross-Region automated backup replication](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_ReplicateBackups.html).

## 7. Optional shared limiter / cache

The minimal authentication design does not need Redis for sessions, OAuth state, memberships, or OTP challenges; PostgreSQL owns those records. Introduce ElastiCache for Valkey or Redis OSS only when a shared rate limiter or measured cache workload justifies it.

If introduced, use a replication group with encryption at rest/in transit and authenticated users, and isolate namespaces/credentials by workload. Cache loss must not restore a revoked session, make an OTP valid, or lose durable customer data. Define whether throttling fails open or closed for each public route and test TLS certificate verification, DNS failover, reconnection, and credential rotation.

AWS references: [ElastiCache replication](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/Replication.html), [ElastiCache resiliency](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/disaster-recovery-resiliency.html), and [in-transit encryption](https://docs.aws.amazon.com/AmazonElastiCache/latest/dg/in-transit-encryption.html).

## 8. Event transport and NATS JetStream decision

### Decision

**Recommended target: introduce an event-transport interface and migrate production fan-out to Amazon SNS topics plus one Amazon SQS queue and dead-letter queue per consumer.** Consumers remain idempotent; the transactional PostgreSQL outbox remains the source for publishing state. Use standard queues when per-key ordering is not required, or FIFO queues only where strict ordering/deduplication is a proven requirement.

Reasons:

- AWS does not offer managed NATS. Amazon MQ currently supports ActiveMQ and RabbitMQ, so it is not a drop-in JetStream host.
- SNS/SQS removes ownership of a stateful three-node messaging cluster, supports independent consumer backlogs, and exposes queue age/depth and redrive behavior.
- SQS is at-least-once: every consumer must use stable event IDs and durable idempotency records. Visibility timeouts must exceed or be extended for processing, and poison messages need DLQs plus redrive/runbook ownership.
- FIFO deduplication has a five-minute interval; it is not a substitute for application idempotency.

### Transition choices

| Choice | Production position |
|---|---|
| SNS + SQS per consumer | **Recommended AWS-native target.** Requires an adapter and contract/integration tests before GA. |
| Three-node JetStream on EC2 with persistent EBS across AZs | Temporary/exception path only. Requires NATS-specific backup, quorum, upgrade, monitoring, failure, and restore ownership. Do not run a single stateful JetStream task on Fargate and call it durable. |
| Externally managed NATS | Requires vendor, legal, security, residency, private-connectivity, SLA, and exit reviews; outside this AWS-only baseline. |
| Core NATS fallback | **Forbidden in production.** Startup/readiness must fail rather than silently losing durable semantics. |

No consumer should treat broker retention as the only business-data copy. Track outbox oldest age, publish failures, queue oldest age, DLQ count, processing latency, attempts, and idempotency conflicts. Write replay tools that can target a bounded tenant/time/event range with authorization and an audit trail.

AWS references: [Amazon MQ engine types](https://docs.aws.amazon.com/amazon-mq/latest/developer-guide/welcome.html), [Amazon MQ broker engines API](https://docs.aws.amazon.com/amazon-mq/latest/api-reference/broker-engine-types.html), [SQS FIFO deduplication](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/FIFO-queues-exactly-once-processing.html), [SQS visibility timeout and at-least-once delivery](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-visibility-timeout.html), and [SNS-to-SQS filtering](https://docs.aws.amazon.com/sns/latest/dg/fifo-message-filtering.html).

## 9. S3 object storage

The site buckets are deployment artifacts; customer content belongs in a different private bucket. Use a per-environment bucket with Block Public Access, Bucket owner enforced, versioning, SSE-KMS, and an S3 Bucket Key where the key/cost model is approved.

Recommended upload flow:

1. Authenticated client asks the API for an upload intent. The API authorizes tenant, object type, expected size/media type, and quota.
2. API creates a random immutable object key under a tenant-scoped quarantine prefix and returns a short-lived presigned `PUT` with checksum and content constraints.
3. Client uploads directly to S3. Presigned URLs are bearer credentials; never log them or store them as stable database fields.
4. GuardDuty Malware Protection for S3 scans the configured quarantine prefix. Process its EventBridge result idempotently because events are at-least-once.
5. Only `NO_THREATS_FOUND` objects are promoted/tagged into an application-readable clean prefix. Unscanned, failed, and malicious objects remain unreadable to application roles and are quarantined/expired.
6. The database stores bucket, key, optional version ID, tenant, owner, checksum, byte size, media type, scan state, retention class, and timestamps—not a presigned URL.
7. Downloads use a newly authorized short-lived presigned `GET` or an application-controlled delivery path.

Use lifecycle rules for abandoned multipart uploads, quarantined objects, temporary exports, and noncurrent versions. Do not overwrite tenant objects at predictable keys. If S3 data-event logging is enabled, scope it to high-risk prefixes because it can materially increase CloudTrail cost.

Current CSV imports are small and database-backed; moving large imports/exports to this path is a planned application change, not an already delivered feature.

AWS references: [S3 presigned URLs](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html), [S3 security best practices](https://docs.aws.amazon.com/AmazonS3/latest/userguide/security-best-practices.html), [GuardDuty Malware Protection for S3 flow](https://docs.aws.amazon.com/guardduty/latest/ug/how-malware-protection-for-s3-gdu-works.html), and [configuring Malware Protection for S3](https://docs.aws.amazon.com/guardduty/latest/ug/configuring-malware-protection-for-s3-guardduty.html).

## 10. Secrets, KMS, and IAM

Store database credentials/DSNs, the OTP HMAC pepper, SMTP credentials, Google account-login and provider OAuth client secrets, integration token-encryption keys, webhook verification secrets, and push-provider credentials in Secrets Manager. Public runtime endpoints are configuration, not secrets. Native CRM sessions are opaque random values, so there is no JWT signing/JWKS key ring.

- Use separate ECS task roles for `crm-api`, each worker class, and migrations. Restrict secret ARNs, KMS decrypt permissions, S3 prefixes, and queue/topic actions.
- Prefer RDS-managed master credentials in Secrets Manager, but do not give applications the master secret. Applications use lower-privilege runtime users.
- Use customer-managed KMS keys by environment and meaningful data boundary, not one key per secret. Separate production from non-production keys and policies.
- Apply least privilege in both IAM policy and KMS key policy. Enable key rotation where supported, protect deletion with change controls, and audit KMS use with CloudTrail.
- Secret values injected as environment variables are visible to the container process and do not refresh in running tasks. A rotation workflow must force a new deployment and tolerate overlap between old/new credentials, or the service must retrieve/cache the secret safely at runtime.
- Never put a secret in a GitHub secret if GitHub can instead assume a scoped AWS role and read/deploy without exposing the value.

AWS references: [Secrets Manager values in ECS tasks](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/secrets-envvar-secrets-manager.html), [RDS-managed master passwords](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/rds-secrets-manager.html), [managed secret rotation](https://docs.aws.amazon.com/secretsmanager/latest/userguide/rotate-secrets_managed.html), [Secrets Manager encryption](https://docs.aws.amazon.com/secretsmanager/latest/userguide/security-encryption.html), [KMS least privilege](https://docs.aws.amazon.com/kms/latest/developerguide/least-privilege.html), and [KMS CloudTrail logging](https://docs.aws.amazon.com/kms/latest/developerguide/logging-using-cloudtrail.html).

## 11. Webhooks, lead capture, and mobile

### Provider callbacks and public capture

Expose callbacks only under deliberate `crmapi.savvyadvi.com` paths, for example `/integrations/<provider>/webhooks`. At the edge and application boundary:

- Use WAF managed rules plus rate-based rules scoped by hostname/path. Keep provider-specific allowlists only when the provider publishes stable ranges and operationally supports them.
- Preserve the exact raw body needed for signature verification. Validate the path token, signature, timestamp/replay window, body limit, content type, and provider event ID before accepting work.
- Persist a small webhook receipt/idempotency record and enqueue durable work before returning a fast 2xx. Slow provider API calls and CRM workflows run asynchronously.
- Treat retries and out-of-order events as normal. Define unique constraints on provider/account/event ID and state transitions that are safe to repeat.
- Redact authorization headers, signatures, raw lead payloads, phone/email content, and query tokens from logs. Store only the minimum payload required, encrypted and retention-bound.
- Separate secrets and callback registrations per provider account and environment. Never send a staging callback to production or reuse production Meta/Google/WhatsApp credentials in development.

The existing generic HMAC lead-capture endpoint is a useful base, but horizontally safe throttling, durable receipt processing, and provider-specific verification are still launch work.

### Mobile applications

iOS and Android Flutter apps use the same `crmapi.savvyadvi.com` service; do not build a second mobile-only backend.

- Email login reuses the challenge-request semantics but needs a dedicated native completion that issues a Bearer session; it must not try to extract the web's HttpOnly cookie. For Google sign-in, launch the CRM API's Google flow in the system browser and complete a short-lived, one-time app-link exchange; the Google client secret remains on the server.
- Host Apple universal-link and Android app-link association files from the application CloudFront origin with exact content types and short cache TTLs.
- Store the returned opaque session token in iOS Keychain/Android Keystore and send it as a Bearer token. There are no access/refresh JWTs; PostgreSQL remains authoritative for session revocation, tenant membership, roles, and permissions.
- Add an authenticated, tenant-aware device-registration API. Store provider token, platform, app/environment, user, device identifier, last-seen, and revocation state.
- Send push messages from a worker, not from request handlers. Keep PII and sensitive lead details out of the notification body; deep-link into an authenticated fetch.
- Keep APNs/FCM credentials in Secrets Manager and scope worker IAM/secret access. Handle token rotation, uninstall/invalid-token feedback, user opt-out, and tenant/user removal.

AWS references: [AWS WAF rate-based rules](https://docs.aws.amazon.com/waf/latest/developerguide/ddos-app-layer-web-ACL-and-rbr.html), [web ACLs](https://docs.aws.amazon.com/waf/latest/developerguide/web-acl.html), and [mobile push endpoints with Amazon SNS](https://docs.aws.amazon.com/sns/latest/dg/sns-mobile-application-as-subscriber.html). Using SNS for push is optional; direct APNs/FCM workers are also compatible with this topology.

## 12. Observability and operations

Build one production dashboard and owned alert route before traffic. At minimum collect:

- **Edge/ALB:** request rate, p50/p95/p99 target latency, 4xx/5xx by target group, rejected connections, healthy-host count, TLS/WAF blocks.
- **ECS:** desired/running/pending tasks, CPU, memory, restarts, OOM exits, deployment failures, autoscaling activity, task-launch failures.
- **PostgreSQL:** CPU, free memory/storage, connections versus budget, read/write latency, locks, slow queries, failovers, backup failures.
- **ElastiCache:** memory, evictions, connections, replication lag, failover events, authentication errors.
- **Async work:** outbox oldest age, publish errors, queue depth/oldest age, DLQs, retries, job duration, sync/workflow lag.
- **Business/security:** login failure spikes, courier failures, webhook signature failures, rate limiting, lead-capture failures, provider-token expiry, push invalid-token rate.

Keep `/metrics` private. Use the existing JSON logs and OpenTelemetry instrumentation, an ADOT collector/sidecar or controlled internal scraper, and CloudWatch alarms. AWS recommends OpenTelemetry for new X-Ray instrumentation; do not introduce new dependencies on the X-Ray SDK/daemon whose maintenance-mode date AWS documents as February 25, 2026.

Operational requirements:

- Structured logs to separate CloudWatch log groups per service/environment; encrypt and set explicit retention (starting proposal: 30 days non-production, 90 days production, longer security archive only if policy requires it).
- No PII, tokens, cookies, credentials, provider payloads, or presigned URLs in application, ALB query, or WAF logs. Validate the repository redaction behavior in production-like tests.
- ALB access logs and WAF logs to dedicated protected destinations with lifecycle rules. AWS documents that the ALB access-log S3 bucket encryption option is SSE-S3.
- CloudTrail organization trail for management events, protected log storage, alerting on security-sensitive changes, and selectively scoped data events.
- Traces propagate a request/correlation ID across API, outbox/event, and worker boundaries without using customer data as an attribute.
- Pager alerts need an owner, severity, runbook, and test. Tickets are sufficient for capacity warnings; loss of auth, writes, durable publishing, all healthy targets, or backups should page.
- Define SLOs for application availability, authentication availability, request latency, webhook acceptance, and async completion. Do not publish an SLA until measurement and incident practice support it.

AWS references: [CloudWatch Logs retention](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/Working-with-log-groups-and-streams.html), [ECS Container Insights](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/deploy-container-insights-ECS-cluster.html), [ADOT sidecar configuration](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/trace-data-containerdefinitions.html), [X-Ray and OpenTelemetry guidance](https://docs.aws.amazon.com/xray/latest/devguide/xray-instrumenting-your-app.html), [ALB access logs](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/enable-access-logging.html), [ALB CloudWatch metrics](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/load-balancer-cloudwatch-metrics.html), and [creating a CloudTrail trail](https://docs.aws.amazon.com/awscloudtrail/latest/userguide/cloudtrail-create-and-update-a-trail.html).

## 13. CI/CD and database migrations

Continue using GitHub Actions, but remove long-lived AWS access keys:

1. GitHub Actions assumes a narrowly scoped deployment role through the AWS IAM OIDC provider. Restrict the trust policy to the exact organization, repository, branch/tag, and protected GitHub environment.
2. Test, lint, audit, and build each service once. Generate an image per deployable process/mode and push it to ECR.
3. Enable ECR enhanced scanning where approved, immutable production tags, lifecycle policies, and deployment by image digest. Block release on the agreed severity/exception policy.
4. Deploy IaC through reviewed CDK/CloudFormation changes. Separate infrastructure, migration, and service-deploy roles.
5. Deploy the tested digest to staging, run smoke/integration/webhook tests, approve production, and promote the same digest—never rebuild it.
6. Run `db-migrate` as a one-off ECS task using a migration-only role and secret. Use an advisory lock and stop the release if migration fails.
7. Follow expand/migrate/contract: additive/backward-compatible schema first, deploy code, backfill asynchronously, observe, then remove old schema in a later release. Never run automatic down migrations.
8. Use ECS rolling deployment with circuit breaker and alarm rollback at launch. Adopt blue/green only after its additional target groups, capacity, and migration rules are operationally justified.
9. Upload static assets with correct cache headers, switch mutable entry points last, run a smoke test, and invalidate only those entry points.

An application rollback selects the previous immutable digest. It is safe only while the database schema remains backward compatible. The pipeline must record commit, image digest, migration version, operator/approval, timestamps, and smoke-test outcome.

AWS references: [GitHub Actions as an IAM OIDC provider](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_create_for-idp_oidc.html), [ECR image scanning](https://docs.aws.amazon.com/AmazonECR/latest/userguide/image-scanning.html), [immutable ECR tags](https://docs.aws.amazon.com/AmazonECR/latest/userguide/image-tag-mutability.html), [ECR lifecycle policies](https://docs.aws.amazon.com/AmazonECR/latest/userguide/LifecyclePolicies.html), [standalone ECS tasks](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/standalone-tasks.html), and [ECS deployment action in CodePipeline](https://docs.aws.amazon.com/codepipeline/latest/userguide/action-reference-ECS.html). CodePipeline is an available alternative, not a requirement for this GitHub-based repository.

## 14. Backup and disaster recovery

Multi-AZ is high availability inside one Region; it is not disaster recovery.

### Backup policy proposal

- Enable RDS automated backups and point-in-time recovery for the CRM database, including native-auth tables. Set 14 days as the minimum launch retention and choose 35 days when compliance/cost permits.
- Take final retained snapshots before destructive database replacement/deletion. Protect production resources from accidental deletion through IaC and change controls.
- Use AWS Backup plans for scheduled retention tiers and cross-account/cross-Region copies where supported and required. Encrypt destination copies with keys controlled in the destination.
- If easy cross-Region automated RDS backup replication is a requirement, retain the Multi-AZ DB **instance** choice and verify support in `<region>`/`<dr-region>`; AWS documents limitations for Multi-AZ DB clusters.
- Enable versioning for content S3. Add cross-Region replication only after the content RPO, residency, KMS, deletion propagation, and cost decisions are explicit.
- Treat ElastiCache as reconstructible. Snapshots can shorten recovery, but application correctness must not depend on restoring cache state.
- Retain the PostgreSQL outbox long enough to reconcile/replay messages after queue failure. Queue/DLQ retention is not a database backup.
- Back up the ability to recover critical secrets, especially the OTP pepper, SMTP credentials, integration encryption key, and Google login secret, under a documented, access-controlled procedure. Database recovery without matching secrets is not complete service recovery.

### Restore and DR testing

- Define provisional RPO and RTO for the CRM database (including auth data), content objects, and authentication availability before selecting final retention/cross-Region spend.
- Run automated restore testing at least quarterly into an isolated account/VPC. Validate schema, row counts/invariants, tenant isolation, authentication, sample object checksums, and application smoke tests—not merely that AWS reports a restored resource.
- Use IaC to recreate networking, ECS, policies, alarms, and edge configuration in `<dr-region>`. Document DNS cutover, certificate/secrets/key dependencies, provider webhook re-registration, mobile endpoint behavior, and failback.
- Perform an annual regional recovery exercise after launch. Record actual RPO/RTO and close gaps; do not claim targets that have not been drilled.

AWS references: [RDS automated backups and PITR](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/USER_WorkingWithAutomatedBackups.html), [AWS Backup plans](https://docs.aws.amazon.com/aws-backup/latest/devguide/creating-a-backup-plan.html), [AWS Backup restore testing](https://docs.aws.amazon.com/aws-backup/latest/devguide/restore-testing.html), and [enabling cross-Region automated backup replication](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/AutomatedBackups.Replicating.Enable.html).

## 15. Launch sizing and cost phases

These are starting hypotheses, not a quote or capacity guarantee. Build an AWS Pricing Calculator estimate in the selected Region using measured request rate, egress, storage, database IOPS, NAT traffic, log ingestion, WAF requests, queue operations, and backup retention. Load test OTP request/verification and session lookup, lead capture, search, CSV import/export, workflow execution, and provider sync before final sizing.

### Phase A — development and staging

- One Fargate task per service/mode, usually 0.25–0.5 vCPU and 0.5–1 GiB; size the CRM API from authentication and CRM workload tests.
- Single-AZ small RDS only where data loss/downtime is acceptable; omit a cache until measured demand requires one.
- One NAT gateway or controlled public egress; scheduled shutdown where the test plan permits.
- Reduced retention and sampled traces.

This phase is not production and must not be presented as HA.

### Phase B — production launch baseline

- Two AZs, one internet-facing ALB for the CRM API/auth routes, and one NAT gateway per AZ.
- `crm-api`: 2 tasks at 0.5 vCPU / 1 GiB as a starting point.
- CRM worker modes: begin at 1 task per ownership-safe worker group, typically 0.5 vCPU / 1 GiB, then scale on backlog/oldest age. Keep unsafe singleton loops explicit.
- RDS: one Multi-AZ PostgreSQL DB instance, beginning with a Graviton burstable class such as `db.t4g.medium` where supported, with gp3 storage and autoscaling.
- ElastiCache: omit at launch unless shared limiter testing demonstrates it is required; if added, use a small Multi-AZ replication group sized from measured traffic.
- Two private CloudFront/S3 sites, one content bucket, WAF on the regional ALB, production logging/alarms, managed backups, and restore tests.

### Phase C — measured growth

- Target-track the CRM API at minimum two tasks; scale workers independently on queue/outbox lag.
- Increase database/cache class only from observed headroom and load tests; add RDS Proxy when connection/failover evidence supports it.
- Split a shared database or ALB if blast radius, scaling, or security justifies the fixed cost.
- Add cross-Region warm capacity only after RPO/RTO and business impact justify it.
- Purchase Compute Savings Plans and database/cache reservations only after 30–60 days of stable production utilization.

The likely fixed-cost drivers at launch are Multi-AZ RDS, NAT gateways, ALB, WAF, and observability—not the low-traffic marketing S3 bucket. An optional replicated cache would add another fixed cost. VPC endpoints can reduce NAT data processing but add hourly charges; calculate both designs. CloudFront can reduce origin/egress load but still incurs request/data transfer cost.

Apply and activate cost allocation tags such as `Application=SavvyCRM`, `Environment`, `Service`, `Owner`, and `CostCenter`. Set monthly budgets at account/environment/service levels and anomaly detection alerts before production spend begins.

Official pricing inputs: [AWS Pricing Calculator documentation](https://docs.aws.amazon.com/pricing-calculator/), [Fargate pricing](https://aws.amazon.com/fargate/pricing/), [Elastic Load Balancing pricing](https://aws.amazon.com/elasticloadbalancing/pricing/), [RDS for PostgreSQL pricing](https://aws.amazon.com/rds/postgresql/pricing/), [ElastiCache pricing](https://aws.amazon.com/elasticache/pricing/), [S3 pricing](https://aws.amazon.com/s3/pricing/), [CloudFront pricing](https://aws.amazon.com/cloudfront/pricing/), [Route 53 pricing](https://aws.amazon.com/route53/pricing/), [VPC/NAT pricing](https://aws.amazon.com/vpc/pricing/), [Secrets Manager pricing](https://aws.amazon.com/secrets-manager/pricing/), [CloudWatch pricing](https://aws.amazon.com/cloudwatch/pricing/), and [AWS WAF pricing](https://aws.amazon.com/waf/pricing/). Cost controls: [AWS Budgets best practices](https://docs.aws.amazon.com/cost-management/latest/userguide/budgets-best-practices.html), [activating cost-allocation tags](https://docs.aws.amazon.com/awsaccountbilling/latest/aboutv2/activating-tags.html), and [Cost Anomaly Detection](https://docs.aws.amazon.com/cost-management/latest/userguide/manage-ad.html).

## 16. Decision and launch checklist

### Product/platform decisions

- [ ] Confirm control of `savvyadvi.com` and create the three selected records (`crm`, `crmapp`, and `crmapi`).
- [ ] Select `<region>` and `<dr-region>` from residency, customer location, service availability, latency, and a current cost estimate.
- [ ] Approve production account structure and separation from development/staging.
- [ ] Approve RPO/RTO, backup retention, cross-account/cross-Region copy, and log-retention requirements.
- [ ] Approve the SNS/SQS migration before GA, or approve ownership/cost/runbooks for a durable JetStream exception.
- [ ] Decide direct APNs/FCM delivery versus SNS mobile push and assign push credential ownership.
- [ ] Approve SLOs, alert routing, on-call owner, and incident severity definitions.

### Engineering gates

- [ ] Split CRM API/worker execution modes and prove safe scale/termination behavior for each background workload.
- [ ] Make missing/failed durable event transport fail startup/readiness in production; remove core NATS downgrade.
- [ ] Implement the selected SNS/SQS adapter and DLQ/redrive/idempotency tests, or complete the approved JetStream HA design and recovery drill.
- [ ] Replace process-local public and OTP rate limits with WAF and/or shared limiter behavior before horizontal production scale; if a cache is introduced, test its TLS/failover/rotation behavior.
- [ ] Add private S3 upload intents, checksum validation, malware quarantine, promotion, expiry, and tenant authorization tests.
- [ ] Implement and test iOS/Android one-time auth handoff, opaque-token secure storage, app/universal links, device registration, token revocation, and no-PII push behavior.
- [ ] Run Meta/Facebook/Instagram lead, WhatsApp, Google, email, and other provider callback tests using separate sandbox/staging accounts; verify signatures, retries, duplicates, token expiry, permission loss, and deletion/disconnect.
- [ ] Validate RLS/tenant isolation, CORS, cookie domain/security flags, CSP, provider secret separation, and redaction in a production-like environment.

### Delivery and operations gates

- [ ] Deploy all AWS resources from reviewed IaC; no unrecorded console-only production dependencies.
- [ ] Configure GitHub OIDC trust restrictions, protected production approvals, immutable ECR images, scanning policy, and digest promotion.
- [ ] Prove expand/migrate/contract deployment and application rollback against a restored production-shaped database.
- [ ] Load test the starting task/DB/cache sizes and set pool/autoscaling ceilings from the results.
- [ ] Configure dashboards, alarms, log retention, WAF/ALB/CloudTrail logging, and a tested pager route.
- [ ] Configure automated backups, deletion protection, cross-Region/cross-account copies as approved, and complete a full restore validation.
- [ ] Complete threat modeling, privacy/data-retention review, penetration testing, dependency/container scanning, and provider-security review.
- [ ] Create AWS budgets, anomaly alerts, and an approved calculator estimate for launch/base/peak traffic.
- [ ] Write and exercise runbooks for database failover, task rollback, queue/DLQ recovery, provider outage/token expiry, cache loss, key/secret rotation, compromised webhook secret, and regional recovery.

## 17. Architecture decisions to record as ADRs

Create short, dated ADRs as these choices close:

1. Canonical domain and public hostname contract.
2. Primary/DR Regions and data-residency constraints.
3. Separate versus shared production RDS instances.
4. SNS/SQS migration versus durable JetStream exception.
5. CRM worker partitioning and autoscaling signals.
6. Native-auth cookie/mobile-token contract and shared rate-limit strategy.
7. RPO/RTO, retention, and regional recovery tier.
8. Mobile push provider path and one-time authentication handoff.
9. Edge protection/logging policy and PII-safe retention.
10. SLOs, incident ownership, and cost guardrails.

This document should be updated when those ADRs are accepted; deployment manifests must reference the accepted decisions rather than silently choosing defaults.
