# Context Effectiveness Analytics Design

## Purpose

Flightdeck currently measures service usage: tool calls, errors, latency, result size, search hits, fallback rescues, and embedding coverage. Those signals show whether agents use Flightdeck and whether retrieval works, but they do not show whether retrieved context improved a decision.

This first iteration adds structured, agent-reported context-impact events and includes their aggregate outcomes in the existing usage report. It is intentionally backend-only. It creates an auditable measurement foundation without claiming causal proof.

## Scope

The change will:

- add a durable `context_impact_events` table;
- expose a write-scoped REST endpoint and MCP tool for recording an impact;
- expose a read-scoped REST endpoint for auditing raw events;
- add context-effectiveness aggregates to the existing REST and MCP usage report;
- document the reporting contract for agents; and
- preserve all existing usage-report fields and behavior.

The change will not add a web dashboard, automatically infer impact from tool-call volume, or implement an A/B evaluation harness.

## Alternatives Considered

### Dedicated impact events — selected

A purpose-built append-only analytics record keeps outcome measurement separate from project activity. It supports strict validation, idempotent writes, raw-event auditing, and stable aggregates without polluting context shown to future agents.

### Activity metadata

Agents could log context outcomes through existing activity metadata. This requires less schema and API work, but it mixes analytics with durable project memory, causes context feeds and embeddings to contain measurement noise, and makes validation and aggregation fragile.

### Tool-call inference

Flightdeck could infer benefit from context reads, searches, and subsequent writes. This has no caller burden, but proximity does not establish that retrieved context was used or that it helped. It would repeat the current mistake of treating usage as value.

## Data Model

Each row in `context_impact_events` represents one reported effect of context within a caller-defined work session.

| Field | Type | Contract |
|---|---|---|
| `id` | UUID | Server generated. |
| `recorded_at` | timestamp | Server generated. |
| `actor` | text | Derived from the authenticated API key. |
| `session_id` | text | Required caller-generated identifier shared by events from one work session. |
| `project` | text | Required existing project slug, stored as a snapshot so analytics survive project deletion. |
| `item` | text, nullable | Optional verified item reference stored as a snapshot. The item must belong to `project`. |
| `effect` | text | `helpful`, `neutral`, or `harmful`. |
| `mechanism` | text | One of the mechanisms below. |
| `context_refs` | text array | Identifiers for summaries, items, activities, or other Flightdeck records that influenced the report. |
| `evidence` | text | Required concise explanation of what changed and why the effect classification is justified. |
| `estimated_minutes_delta` | integer, nullable | Signed estimate: positive means time saved, negative means time lost. |
| `idempotency_key` | text, nullable | Safe-retry key unique per actor when present. |

The valid effect/mechanism combinations are:

| Effect | Mechanism |
|---|---|
| `helpful` | `decision_changed` |
| `helpful` | `prevented_error` |
| `helpful` | `duplicate_work_avoided` |
| `helpful` | `reconstruction_saved` |
| `neutral` | `ignored` |
| `harmful` | `stale_or_incorrect` |

Helpful events may have a non-negative time delta, harmful events may have a non-positive delta, and neutral events must omit the delta or report zero. Database constraints mirror service validation.

An event is immutable. Retrying a request with the same actor and idempotency key returns the original event without inserting a duplicate. Impact events are retained for longitudinal analysis and are not included in the existing short-lived tool-call purge.

## Interfaces

### REST write

`POST /api/context-impact` requires `write` scope and accepts:

```json
{
  "session_id": "codex-2026-09-03-release-notes",
  "project": "ai-voice",
  "item": "ai-voice-17",
  "effect": "helpful",
  "mechanism": "prevented_error",
  "context_refs": ["ai-voice project instructions", "ai-voice-17"],
  "evidence": "The stored product-name rule prevented legacy copy in the release notes.",
  "estimated_minutes_delta": 15,
  "idempotency_key": "codex-2026-09-03-release-notes-product-name"
}
```

It returns `201 Created` for a new event and `200 OK` with the original event for an idempotent retry.

### REST audit

`GET /api/context-impact` requires `read` scope. It accepts `days` (default 7, maximum 90), optional `project`, and `limit` (default 100, maximum 500), and returns raw events newest first.

### MCP write

`record_context_impact` exposes the same fields as the REST write endpoint. The authenticated MCP actor is recorded automatically. There is no MCP list tool in this iteration; raw auditing remains available through REST and aggregate results through `usage_report`.

## Usage Report Aggregates

The existing `usage_report` response gains a `context_effectiveness` object. All fields are additive, and existing fields retain their names and semantics.

```json
{
  "measurement_basis": "reported_impacts",
  "reported_sessions": 12,
  "helpful_sessions": 8,
  "neutral_sessions": 3,
  "harmful_sessions": 1,
  "contribution_rate": 0.6667,
  "prevented_error_sessions": 3,
  "prevented_error_rate": 0.25,
  "duplicate_work_avoided_sessions": 2,
  "duplicate_work_avoidance_rate": 0.1667,
  "harm_rate": 0.0833,
  "estimated_minutes_net": 120,
  "net_context_value": 4
}
```

Calculations use distinct `(actor, session_id)` pairs within the selected window so two agents cannot accidentally merge sessions that use the same caller-generated identifier:

- contribution rate = sessions with at least one helpful event / reported sessions;
- prevented-error rate = sessions with a `prevented_error` event / reported sessions;
- duplicate-work avoidance rate = sessions with a `duplicate_work_avoided` event / reported sessions;
- harm rate = sessions with a harmful event / reported sessions;
- estimated minutes net = sum of all reported signed time deltas; and
- net context value = prevented-error sessions + duplicate-work-avoided sessions - harmful sessions.

A session can be both helpful and harmful, so category counts and rates are intentionally not mutually exclusive. A report with no events returns zero counts and rates rather than nulls or division errors. `measurement_basis` prevents consumers from presenting these self-reported aggregates as causal proof.

## Validation and Errors

The service requires a trimmed session ID of 1–200 characters and evidence of 1–2,000 characters. It accepts at most 20 context references of at most 200 characters each, an optional idempotency key of at most 200 characters, and an optional time delta from -1,440 to 1,440 minutes. It also validates project existence, optional item ownership, effect, mechanism, effect/mechanism compatibility, and the time-delta sign.

REST returns `400 Bad Request` for invalid input, `404 Not Found` for an unknown project or item, and the existing database-error response for storage failures. MCP returns the same validation meaning through its tool error. Analytics writes are synchronous because the caller deliberately records an outcome; failures are visible rather than silently discarded.

## Testing

Tests will cover:

- every valid effect/mechanism combination;
- invalid combinations and invalid signed time deltas;
- required and bounded text fields;
- item-to-project ownership validation;
- idempotent retries;
- raw-event filtering and ordering;
- aggregate counts, overlapping helpful/harmful sessions, rates, signed time totals, and net context value;
- zero-event reports; and
- preservation of existing usage-report fields.

The implementation follows test-driven development: each behavior is introduced by a failing test, implemented minimally, and rerun alongside the full Go and web verification commands before the PR is opened.

## Interpretation and Follow-up

These metrics answer, “What impact did agents report after using context?” They do not answer, “What would have happened without context?” Establishing that causal difference requires a later retrospective or randomized evaluation using comparable tasks with and without Flightdeck context.

The first iteration is successful when agents can report outcomes consistently, raw reports are auditable, and the usage report clearly exposes both helpful and harmful rates without implying causation.
