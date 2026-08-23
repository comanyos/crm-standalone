# Workflow automation — v1 design boundary

This is the implementation contract for the first automation increment. It
does not enable rules yet: enabling automation without a durable execution
ledger, bounded retries, and an idempotency model would make duplicate outbox
delivery visible as duplicate customer work.

## Existing foundation

CRM already publishes durable, tenant-labelled envelopes through its
transactional outbox. Delivery is at-least-once, so every eventual workflow
execution must use `(tenant_id, rule_version_id, source_event_id)` as its
idempotency key. The event payload is a snapshot for rule evaluation only;
actions must re-read their target under the execution tenant scope before
they mutate it.

## First supported model

The first release will support a single record-triggered rule with one or
more ordered actions:

- Trigger: an allowlisted CRM event type and object type.
- Conditions: an AND-only, typed comparison over allowlisted fixed fields.
- Actions: create task, assign task, add activity, and create in-app
  notification.
- States: draft, active, paused, archived. Editing an active rule creates a
  new immutable version; executions retain the version they used.

No arbitrary code, template expressions, outbound webhooks, or time-based
recurrence belongs in v1. Those require separate sandboxing, secret, egress,
and scheduling policies.

## Required storage and controls

Every table will be tenant-owned and RLS-protected: `workflow_rules`,
`workflow_rule_versions`, `workflow_executions`, and `notifications`. An
execution records source event, attempt count, outcome, safe error category,
and timestamps; it never stores OAuth tokens, request headers, or full CRM
records. Rule authors need a dedicated write scope and ordinary users may
read only notifications addressed to them.

Each event may start at most one execution per active rule version. Execution
has a bounded retry policy and terminal `failed` state. Rules cannot trigger
on their own output in v1, preventing loops by construction; later
multi-step rules must add explicit causation-chain depth limits.

## Decisions still needed before integrations

Email/SMS, Slack/Teams, Google/Microsoft, and outbound webhooks remain out of
scope until a product owner supplies provider choices, OAuth/webhook secrets,
retention/deletion policy, and production secret/KMS ownership. Provider
credentials must be encrypted at rest, unavailable to the browser, and
purged on disconnect.
