# Context Effectiveness Analytics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add auditable, agent-reported context-impact events and effectiveness aggregates to Flightdeck's REST API, MCP server, and existing usage report.

**Architecture:** Store immutable impact events in a dedicated PostgreSQL table so analytics do not pollute durable project activity. A focused service validates and records idempotent events, REST and MCP expose the write contract, REST exposes raw audit reads, and `UsageReport` calculates session-based aggregates with an explicit `reported_impacts` measurement label.

**Tech Stack:** Go, PostgreSQL 17, sqlc, `net/http`, Model Context Protocol Go SDK, goose migrations, standard Go testing.

## Global Constraints

- This iteration is backend-only; do not add a web dashboard.
- Reported impact is not causal proof; every aggregate must carry `measurement_basis: "reported_impacts"`.
- Preserve every existing `usage_report` field and semantic.
- Keep impact events outside project activity and semantic embeddings.
- Project and item snapshots must remain auditable after their source records are deleted.
- Follow test-driven development: observe each focused test fail before adding its production behavior.

---

### Task 1: Persistence and domain validation

**Files:**
- Create: `migrations/00013_context_effectiveness.sql`
- Create: `internal/store/queries/context_impact.sql`
- Create: `internal/service/context_impact.go`
- Modify: `internal/service/validate.go`
- Modify: `internal/service/validate_test.go`
- Create: `internal/integration/context_impact_test.go`
- Regenerate: `internal/store/context_impact.sql.go`
- Regenerate: `internal/store/models.go`
- Regenerate: `internal/store/querier.go`

**Interfaces:**
- Produces `service.ContextImpactInput`, `service.ValidateContextImpact`, `service.ErrInvalidContextImpact`, and `(*Service).RecordContextImpact(ctx, input, actor) (store.ContextImpactEvent, bool, error)`.
- Produces sqlc methods `CreateContextImpactEvent`, `GetContextImpactByIdempotencyKey`, `ListContextImpactEvents`, and `ContextEffectivenessSummary`.
- The returned `bool` is true only when a row was inserted and false for an idempotent replay.

- [ ] **Step 1: Write validation tests that define the domain contract**

Add table-driven tests to `internal/service/validate_test.go` covering all six valid effect/mechanism pairs, invalid pairs, empty and oversized session/evidence fields, more than 20 references, oversized references and idempotency keys, and helpful/neutral/harmful time-delta signs.

```go
func TestValidateContextImpact(t *testing.T) {
	valid := []ContextImpactInput{
		{SessionID: "s1", Project: "alpha", Effect: "helpful", Mechanism: "decision_changed", Evidence: "changed the plan"},
		{SessionID: "s1", Project: "alpha", Effect: "helpful", Mechanism: "prevented_error", Evidence: "avoided a bad write"},
		{SessionID: "s1", Project: "alpha", Effect: "helpful", Mechanism: "duplicate_work_avoided", Evidence: "found prior work"},
		{SessionID: "s1", Project: "alpha", Effect: "helpful", Mechanism: "reconstruction_saved", Evidence: "loaded prior state"},
		{SessionID: "s1", Project: "alpha", Effect: "neutral", Mechanism: "ignored", Evidence: "context was unrelated"},
		{SessionID: "s1", Project: "alpha", Effect: "harmful", Mechanism: "stale_or_incorrect", Evidence: "context was wrong"},
	}
	for _, in := range valid {
		if err := ValidateContextImpact(in); err != nil {
			t.Errorf("valid input rejected: %v", err)
		}
	}
}
```

- [ ] **Step 2: Run the validation test and verify RED**

Run: `go test ./internal/service -run TestValidateContextImpact -count=1`

Expected: compilation fails because `ContextImpactInput` and `ValidateContextImpact` do not exist.

- [ ] **Step 3: Implement minimal constants, input type, and validation**

Define the input in `internal/service/context_impact.go`:

```go
type ContextImpactInput struct {
	SessionID             string
	Project               string
	Item                  *string
	Effect                string
	Mechanism             string
	ContextRefs           []string
	Evidence              string
	EstimatedMinutesDelta *int32
	IdempotencyKey        *string
}
```

Add `ValidContextEffects`, `ValidContextMechanisms`, and `ValidateContextImpact`. Trim values before persistence, require session/evidence lengths of 1–200 and 1–2,000 characters, allow at most 20 references of at most 200 characters, limit the optional idempotency key to 200 characters, constrain time delta to -1,440 through 1,440, and enforce the effect/mechanism and sign matrices from the design.

- [ ] **Step 4: Run the validation tests and verify GREEN**

Run: `go test ./internal/service -run TestValidateContextImpact -count=1`

Expected: PASS.

- [ ] **Step 5: Write failing DB-backed tests for persistence, ownership, and idempotency**

Create `internal/integration/context_impact_test.go` with tests that call `RecordContextImpact` twice with one key and assert the second call returns the original ID with `created == false`. Also verify an item from another project returns a validation error and that listing by project/window returns newest events first.

```go
first, created, err := svc.RecordContextImpact(ctx, in, "tester")
if err != nil || !created {
	t.Fatalf("first record = %v, %v", created, err)
}
second, created, err := svc.RecordContextImpact(ctx, in, "tester")
if err != nil || created || second.ID != first.ID {
	t.Fatalf("replay = %+v, %v, %v", second, created, err)
}
```

- [ ] **Step 6: Run the integration tests and verify RED**

Run: `FLIGHTDECK_TEST_DB="$FLIGHTDECK_TEST_DB" go test ./internal/integration -run 'TestRecordContextImpact|TestListContextImpact' -count=1`

Expected: compilation fails because the migration, generated store methods, and service method do not exist.

- [ ] **Step 7: Add the migration and sqlc queries**

Create `migrations/00013_context_effectiveness.sql` with `context_impact_events`, enum and sign CHECK constraints, indexes on `recorded_at`, `project`, and `(actor, session_id)`, plus a partial unique index on `(actor, idempotency_key)` when the key is non-null. Use text snapshots for `project` and `item`; do not add foreign keys.

Create `internal/store/queries/context_impact.sql` with:

```sql
-- name: CreateContextImpactEvent :one
INSERT INTO context_impact_events
    (actor, session_id, project, item, effect, mechanism, context_refs,
     evidence, estimated_minutes_delta, idempotency_key)
VALUES ($1, $2, $3, sqlc.narg('item'), $4, $5, $6, $7,
        sqlc.narg('estimated_minutes_delta'), sqlc.narg('idempotency_key'))
RETURNING *;

-- name: GetContextImpactByIdempotencyKey :one
SELECT * FROM context_impact_events
WHERE actor = $1 AND idempotency_key = $2;

-- name: ListContextImpactEvents :many
SELECT * FROM context_impact_events
WHERE recorded_at >= $1
  AND (sqlc.narg('project')::text IS NULL OR project = sqlc.narg('project'))
ORDER BY recorded_at DESC
LIMIT $2;
```

Add `ContextEffectivenessSummary` as a session-level CTE grouped by `(actor, session_id)` that returns reported/helpful/neutral/harmful/prevented-error/duplicate-work-avoided session counts and the sum of event time deltas.

- [ ] **Step 8: Regenerate sqlc output**

Run: `sqlc generate`

Expected: generated store types and methods compile without manual edits.

- [ ] **Step 9: Implement idempotent persistence and ownership validation**

Implement `RecordContextImpact` by validating and normalizing input, resolving the project, resolving an optional item by UUID or short ref, verifying `item.ProjectID == project.ID`, checking an existing actor/idempotency key, inserting, and recovering from a unique-key race by reading the winning row. Wrap validation and project/item mismatch errors with `ErrInvalidContextImpact`; preserve `pgx.ErrNoRows` for unknown projects or items so transports can distinguish 400 from 404.

- [ ] **Step 10: Run focused persistence tests and verify GREEN**

Run: `FLIGHTDECK_TEST_DB="$FLIGHTDECK_TEST_DB" go test ./internal/integration -run 'TestRecordContextImpact|TestListContextImpact' -count=1`

Expected: PASS.

- [ ] **Step 11: Commit the persistence slice**

```bash
git add migrations/00013_context_effectiveness.sql internal/store internal/service internal/integration/context_impact_test.go
git commit -m "feat: record context impact events"
```

### Task 2: REST and MCP contracts

**Files:**
- Create: `internal/api/context_impact.go`
- Modify: `internal/api/server.go`
- Modify: `internal/mcp/server.go`
- Modify: `internal/dto/dto.go`
- Modify: `internal/integration/context_impact_test.go`
- Modify: `internal/integration/mcp_payload_test.go`

**Interfaces:**
- Consumes `service.ContextImpactInput` and `(*Service).RecordContextImpact` from Task 1.
- Produces `dto.ContextImpactEvent`, `POST /api/context-impact`, `GET /api/context-impact`, and MCP `record_context_impact`.

- [ ] **Step 1: Write failing REST contract tests**

Extend `internal/integration/context_impact_test.go` to start the real API with a read/write key. Assert a valid POST returns 201, an idempotent replay returns 200 with the same ID, a malformed combination returns 400, an unknown project/item returns 404, a cross-project item returns 400, and a filtered GET returns only matching raw events in newest-first order.

- [ ] **Step 2: Run REST tests and verify RED**

Run: `FLIGHTDECK_TEST_DB="$FLIGHTDECK_TEST_DB" go test ./internal/integration -run TestContextImpactHTTP -count=1`

Expected: FAIL with 404 because `/api/context-impact` is not registered.

- [ ] **Step 3: Add DTOs and REST handlers**

Add `dto.ContextImpactEvent` with ID, timestamp, actor, session, project, optional item, effect, mechanism, references, evidence, and optional time delta. Implement `POST /context-impact` with write auth and `GET /context-impact` with read auth. Map `ErrInvalidContextImpact` to 400 and `pgx.ErrNoRows` to 404. Parse GET defaults as 7 days and 100 rows, reject days outside 1–90 and limits outside 1–500, and preserve the 201-new/200-replay distinction.

```go
type ContextImpactEvent struct {
	ID                    uuid.UUID `json:"id"`
	RecordedAt            time.Time `json:"recorded_at"`
	Actor                 string    `json:"actor"`
	SessionID             string    `json:"session_id"`
	Project               string    `json:"project"`
	Item                  *string   `json:"item,omitempty"`
	Effect                string    `json:"effect"`
	Mechanism             string    `json:"mechanism"`
	ContextRefs           []string  `json:"context_refs"`
	Evidence              string    `json:"evidence"`
	EstimatedMinutesDelta *int32    `json:"estimated_minutes_delta,omitempty"`
}
```

- [ ] **Step 4: Run REST tests and verify GREEN**

Run: `FLIGHTDECK_TEST_DB="$FLIGHTDECK_TEST_DB" go test ./internal/integration -run TestContextImpactHTTP -count=1`

Expected: PASS.

- [ ] **Step 5: Write a failing MCP contract test**

Extend `internal/integration/mcp_payload_test.go` to invoke `record_context_impact` against the real MCP server, decode its one text-content JSON result, and assert the stored actor, project, effect, mechanism, evidence, and event ID.

- [ ] **Step 6: Run the MCP test and verify RED**

Run: `FLIGHTDECK_TEST_DB="$FLIGHTDECK_TEST_DB" go test ./internal/integration -run TestMCPRecordContextImpact -count=1`

Expected: FAIL because the MCP tool is not registered.

- [ ] **Step 7: Register and implement the MCP tool**

Add `recordContextImpactIn`, register `record_context_impact`, and route its handler through `service.RecordContextImpact`. Its description must state that it records reported—not proven—impact and should be called for helpful, ignored, or harmful context outcomes. Return `dto.ContextImpactEvent` using the existing single-text-result wrapper.

- [ ] **Step 8: Run REST and MCP tests and verify GREEN**

Run: `FLIGHTDECK_TEST_DB="$FLIGHTDECK_TEST_DB" go test ./internal/integration -run 'TestContextImpactHTTP|TestMCPRecordContextImpact' -count=1`

Expected: PASS.

- [ ] **Step 9: Commit the transport slice**

```bash
git add internal/api internal/mcp internal/dto internal/integration
git commit -m "feat: expose context impact reporting"
```

### Task 3: Effectiveness aggregates and documentation

**Files:**
- Modify: `internal/dto/dto.go`
- Modify: `internal/service/usage.go`
- Modify: `internal/integration/audit_fixes_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes `ContextEffectivenessSummary` from Task 1.
- Extends `dto.UsageReport` with `ContextEffectiveness dto.ContextEffectiveness` under JSON key `context_effectiveness`.

- [ ] **Step 1: Write failing aggregate tests**

Extend `TestUsageAnalytics` with events from multiple actors and repeated session IDs. Include one mixed helpful/harmful session and assert reported/helpful/harmful session counts, contribution/prevented-error/duplicate-avoidance/harm rates, net signed minutes, and net context value. Add a zero-event assertion that every rate and count is zero while the measurement basis remains populated.

- [ ] **Step 2: Run aggregate tests and verify RED**

Run: `FLIGHTDECK_TEST_DB="$FLIGHTDECK_TEST_DB" go test ./internal/integration -run TestUsageAnalytics -count=1`

Expected: compilation fails because `UsageReport.ContextEffectiveness` does not exist.

- [ ] **Step 3: Add the effectiveness DTO and usage aggregation**

Define `dto.ContextEffectiveness` with measurement basis, session counts, rates, estimated net minutes, and net context value. Fetch the SQL summary alongside existing usage queries in the `errgroup`. Calculate rates through one zero-safe helper, and calculate net context value as prevented-error sessions plus duplicate-work-avoided sessions minus harmful sessions.

```go
type ContextEffectiveness struct {
	MeasurementBasis             string  `json:"measurement_basis"`
	ReportedSessions             int     `json:"reported_sessions"`
	HelpfulSessions              int     `json:"helpful_sessions"`
	NeutralSessions              int     `json:"neutral_sessions"`
	HarmfulSessions              int     `json:"harmful_sessions"`
	ContributionRate             float64 `json:"contribution_rate"`
	PreventedErrorSessions       int     `json:"prevented_error_sessions"`
	PreventedErrorRate           float64 `json:"prevented_error_rate"`
	DuplicateWorkAvoidedSessions int     `json:"duplicate_work_avoided_sessions"`
	DuplicateWorkAvoidanceRate   float64 `json:"duplicate_work_avoidance_rate"`
	HarmRate                     float64 `json:"harm_rate"`
	EstimatedMinutesNet          int     `json:"estimated_minutes_net"`
	NetContextValue              int     `json:"net_context_value"`
}
```

- [ ] **Step 4: Run aggregate tests and verify GREEN**

Run: `FLIGHTDECK_TEST_DB="$FLIGHTDECK_TEST_DB" go test ./internal/integration -run TestUsageAnalytics -count=1`

Expected: PASS.

- [ ] **Step 5: Document the agent contract and interpretation boundary**

Update `README.md` to list `POST/GET /context-impact`, the MCP `record_context_impact` tool, valid effect/mechanism combinations, and the additive `context_effectiveness` report. State that the metrics are self-reported and require a later controlled evaluation for causal claims.

- [ ] **Step 6: Run complete verification**

Run:

```bash
gofmt -w internal/api/context_impact.go internal/service/context_impact.go internal/service/validate.go internal/service/validate_test.go internal/dto/dto.go internal/mcp/server.go internal/integration/context_impact_test.go internal/integration/audit_fixes_test.go internal/integration/mcp_payload_test.go
git diff --check
go vet ./...
go test ./...
npm --prefix web test
npm --prefix web run build
```

With a disposable PostgreSQL database configured, also run:

```bash
FLIGHTDECK_TEST_DB="$FLIGHTDECK_TEST_DB" go test ./internal/integration -count=1
```

Expected: every command exits zero.

- [ ] **Step 7: Commit the aggregate and documentation slice**

```bash
git add internal/dto/dto.go internal/service/usage.go internal/integration/audit_fixes_test.go README.md docs/superpowers/plans/2026-09-03-context-effectiveness-analytics.md
git commit -m "feat: report context effectiveness"
```

- [ ] **Step 8: Independently review, push, and open the PR**

Build a task-scoped review packet, run the read-only Claude consultation, investigate every substantive finding, rerun affected verification, then push `feature/context-effectiveness-analytics` and open a PR against `main` with the design, behavior, and actual test evidence.
