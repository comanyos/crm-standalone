# Workflow automation — v1

Automation is implemented and runs by default. This document is what it does,
why it is shaped this way, and what it deliberately does not do.

The narrowness is the feature. A rule is one allowlisted trigger, an AND-only
list of typed comparisons over allowlisted fields, and an ordered list of
actions from a fixed set. Each action may carry a bounded delay, which turns
that ordered action list into a restart-safe sequence. A published rule may
also be launched by a durable one-time or recurring wall-clock schedule.
There is no arbitrary code or expression language.

## What runs

`internal/integration/workflow` polls each tenant's outbox on a ten-second
loop (`workflow.Start`, wired in `cmd/crm/main.go`). Unlike the Gmail and
Calendar pollers it has no configuration to be missing: automation is part of
the CRM rather than an integration, so it always runs, and a deployment with
no active rules simply finds no due tenants.

One pass claims the tenant's cursor, reads a bounded batch of new events,
claims one execution per matching active rule version, and then performs
everything due — including delayed sequence steps and retries scheduled by
earlier passes. The cursor is
always released, including on failure; a tenant left leased by a failed pass
would have no automation until the lease expired.

## The two properties everything turns on

**Delivery is at-least-once, so every event arrives more than once.** Nothing
in the engine decides whether work has been done by remembering. The
idempotency key `(tenant_id, rule_version_id, source_event_id)` is the
decision: only the caller that wins the insert acts, and a second delivery is
counted as suppressed. Without that, a second outbox delivery is a second
follow-up call on a customer's record.

**The event payload is a snapshot for evaluating conditions, never a source of
truth for acting.** Every action re-reads its target under the execution's own
tenant scope before it writes, because the record may have been reassigned —
or deleted — between the event and the execution. A test reassigns a lead
after the event and asserts the created task lands on the new owner.

## Triggers, conditions, actions

Triggers (`internal/domain/workflow.go`, allowlisted):

| Event | Object |
| --- | --- |
| `lead.created` | lead |
| `opportunity.stage_changed` | opportunity |
| `opportunity.closed_won` | opportunity |

Conditions compare allowlisted fixed fields — lead `source`, `status`,
`owner_id`, `company`, `score`; opportunity `stage`, `account_id`,
`pipeline_id`, `name`, `amount` — with operators the field's kind allows
(`eq`, `neq`, `contains`, `in` for text; `eq`, `neq`, `gt`, `lt` for numbers).
They combine with AND only: OR needs grouping, grouping needs an expression
tree, and that needs an editor and an evaluator v1 does not have.

Custom fields are deliberately not comparable. They are tenant-defined and can
be archived or retyped underneath a saved rule, which would turn it into one
that silently stops matching; that needs a versioning story of its own.

Actions are `create_task`, `add_activity`, `notify`, `assign_owner`,
`draft_message`, `request_approval`, and `send_webhook`. Every action also has
an optional `delay_minutes` value from zero through one year. The engine writes
the next due instant to the durable execution ledger before returning; another
poll or a restarted process continues only when that instant is due. Delays
are relative to the preceding completed action, so an ordered rule is a manual
follow-up sequence without requiring an in-memory timer.

Assigning a task is
not a separate action, as the original design sketch had it: `create_task`
carries an assignee selector (`record_owner`, resolved against the record as
it is when the action runs, or `fixed_user`), which is the same capability
without a second action that can only be written after the first. Action text
is literal — there is no template expansion, because a template that can read
the record can read fields the rule author is not allowed to see.

A rule is refused at authoring time if it names a trigger, field, operator or
action outside these lists. A rule that will not save is better than one the
author believes is working.

### Scheduled sequences

A schedule points at an active published rule and one existing Account,
Contact, Lead, or Opportunity of the same object type. It stores an absolute
next run plus the IANA time zone that gave that instant meaning. Recurrence is
`once`, `daily`, `weekly`, or `monthly`, with an interval and optional end.
Daily, weekly, and monthly advances happen in local wall-clock time, so a
09:00 schedule remains 09:00 through daylight-saving changes; a monthly run
on the 31st clamps to the last day of shorter months.

The poller turns a due occurrence into the ordinary workflow execution ledger
using `schedule:<id>:<scheduled instant>` as the source-event identity. It
then advances the schedule with an optimistic compare on that instant. A
crash, retry, or second replica therefore cannot create a second execution.
After downtime, one missed occurrence is run and the clock advances directly
to the next future slot instead of creating a burst of stale catch-up work.

### Approvals

`request_approval` resolves either the record owner or a fixed principal at
execution time. It creates one approval per execution/action, notifies that
person, and moves the execution to `waiting_approval`. Only the named
principal can see or decide it. Approval resumes at the same action, where the
durable decision is observed and the sequence proceeds exactly once;
rejection ends the execution with the bounded `approval_rejected` reason.
The decision and optional note are stored in the tenant-scoped audit row.

### Signed outbound webhooks

Administrators register a named public HTTPS endpoint on port 443. Savvy CRM
generates a 256-bit signing secret, returns it once, and stores only ciphertext
bound to both tenant and endpoint id. `WORKFLOW_WEBHOOK_SECRET_KEY` is a
dedicated deployment key; it must not be shared with lead capture or provider
OAuth credentials.

Each delivery is JSON and carries `X-Savvy-Event-ID`, `X-Savvy-Timestamp`, and
`X-Savvy-Signature`. The signature is lower-case hex HMAC-SHA256 over
`<timestamp>.<raw request body>` and is prefixed with `sha256=`. The event id
is stable across retries. Receivers must reject stale timestamps, compare the
signature in constant time, and deduplicate event ids.

The sender refuses HTTP, credentials in URLs, redirects, non-443 ports, IP
literals in private/reserved ranges, and any DNS answer in those ranges. DNS
is checked again in the actual dial path to prevent rebinding. Requests time
out after ten seconds, response bodies are discarded, and only bounded status
and HTTP-code metadata is retained in the delivery audit.

## Versioning and lifecycle

States are draft, active, paused and archived. A rule is always created as a
draft: status is not a parameter, because a rule can only be active once it
has a version to run.

Editing never mutates what is running. `PublishWorkflowRuleVersion` writes a
new immutable version and, without `activate`, changes nothing about what the
rule does — an author correcting a live rule reviews the new version before it
starts acting. That is what makes an execution's ledger row meaningful months
later, after the rule has been edited four times: it names the exact text that
ran.

Archived is terminal. A retired rule that can be switched back on is one that
can start acting on records long after everyone stopped thinking about it;
reinstating means authoring a new rule, which is a decision somebody makes
rather than a checkbox.

No RPC accepts a version id. A rule runs its own versions, the reference
between the two tables is circular so there is no foreign key to enforce it,
and both stores' queries say it explicitly.

## The execution ledger

Every encounter between a rule version and an event is recorded, including the
ones whose conditions did not hold — those are stored as `skipped` rather than
discarded. "Why didn't my rule fire" is the question authors actually ask, and
an engine that keeps no record of considering an event cannot answer it.

Failures are bounded codes, never messages: `record_gone`, `no_assignee`,
`action_failed`, `rule_version_gone`, `invalid_action`. The rows are what a
rule author reads, and a raw error would carry whatever the failing action was
holding. The UI renders each code as a sentence.

Retries are bounded at three attempts per action with an exponential backoff, and they
resume rather than repeat: `actions_completed` records how far the execution
got, so an execution that created the task and then failed to notify does not
create a second task. A deleted record fails terminally on the first attempt —
it will not come back, and retrying twice more only delays the author seeing
it.

A pass whose executions failed reports failure rather than success; one
tenant's failure never ends a poller cycle, but automation a tenant was
promised did not happen, and a silent pass hides that.

## Loop prevention

Structural, not counted. No event a rule may trigger on is one a workflow
action produces (`task.created`, `activity.created`, `notification.created`),
so a rule cannot fire on its own output. `domain.AssertTriggersCannotLoop`
checks that at startup in `server.New` rather than trusting it, because the
property is one added event type away from being false and there is no depth
limit behind it. A later multi-step version that lets rules chain must add an
explicit causation-chain depth limit first.

## Authorization

`crm:workflow:read` and `crm:workflow:write` are their own pair rather than
shared with any entity's. A rule acts on every record its trigger matches, on
behalf of whoever wrote it, until somebody retires it — that is manager
authority, not a consequence of being able to edit opportunities. Members read
rules without authoring them, so a rep can see which rule created the task in
front of them.

Notifications are the caller's own: neither RPC takes a recipient, for the
same reason the Google RPCs take no owner. `crm_viewer` holds
`crm:notification:read` and not its write half, because acknowledging one is
still a write and holding no write authority is that role's defining property.

Every table is tenant-owned and RLS-protected, and every query carries an
explicit `tenant_id` predicate as well. Executions store the source event,
attempt count, outcome, safe error category and timestamps — never tokens,
headers or full records.

## Named open items

- **Chat destinations.** Slack, Teams, and SMS still need provider choices,
  OAuth/secret ownership, retention, and production acceptance. Generic
  signed webhooks are implemented but are not a substitute for those product
  decisions.
- **Templated text.** Needs a field-visibility model, since a template that
  can read the record can read fields the author cannot.
- **Custom fields in conditions.** Needs the definition-versioning story.
- **Cross-rule chaining.** Needs the depth limit named above; ordered delayed
  steps inside one rule are implemented.

## Decisions still needed before integrations

SMS, Slack/Teams, and Microsoft remain out of scope until a product owner
supplies provider choices, OAuth secrets, retention/deletion policy, and
production secret/KMS ownership. Operational email uses the same server-side
SMTP transport as passwordless login. Google Gmail/Calendar, Meta, lead
capture, and outbound workflow webhooks use independent encrypted credential
groups; provider credentials remain unavailable to the browser and are purged
on disconnect.
