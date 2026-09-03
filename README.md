# Flightdeck

A single, cross-project context layer for Nathan + agents. One schema spanning
every project (projects · items · activity), shaped for agents to **read context
→ do work → log what changed**. Go backend, embedded React kanban, MCP server —
all one self-contained binary.

## Run (Docker + Tailscale)

The service is part of `~/docker-services/docker-compose.yml`, on the shared
Postgres 17. It binds to the Tailscale IP only.

```bash
# one-time: create the DB (already added to init-scripts for clean rebuilds)
docker compose exec postgres psql -U postgres -c "CREATE DATABASE flightdeck;"

# build + run
docker compose up -d --build flightdeck
```

- **UI / API / MCP:** http://100.79.61.79:4300 (Tailscale)
- Health: `GET /healthz` (unauthenticated)
- Migrations run automatically on startup (goose, embedded).

## API keys

Generate keys with the `keygen` subcommand (prints the raw key once):

```bash
docker compose exec flightdeck flightdeck keygen "my agent" read,write,ingest
docker compose exec flightdeck flightdeck keygen "site widget" ingest
```

Scopes: `read` (orient), `write` (create/update/log), `ingest` (bug reports only).
Send the key as the `X-API-Key` header. The UI stores it in `localStorage`.

List and revoke keys:

```bash
docker compose exec flightdeck flightdeck keys list
docker compose exec flightdeck flightdeck keys revoke <id>
```

## HTTP API (`/api`)

- `GET/POST /projects`, `GET/PATCH /projects/{slug}`
- `GET/POST /items`, `GET/PATCH/DELETE /items/{id}` (filters: project, status, type, assignee, tag, q, updated_since)
- `GET/POST /activity` (filters: project, item_id, kind, since)
- `GET/POST /context-impact` — audit and record agent-reported helpful, ignored, or harmful context outcomes (`GET` filters: days, project, limit)
- `GET /context/{slug}` — project orient bundle (open items flagged `blocked`/`blocked_by`); `GET /context` — horizontal view
- `GET /next-action?project=&limit=` — ranked open, unblocked items ("what to work on")
- `GET /digest/{slug}?since=` — compact rollup of recent activity (counts, decisions, summary)
- `GET /stale?in_progress_days=&bug_days=` — idle in_progress items, untriaged bugs, stale summaries
- `POST /links` `{from, to, kind}` · `DELETE /links/{id}` · `GET /items/{id}/links` — item dependencies (`blocks`|`relates_to`|`parent_of`)
- `POST /items/{id}/refs` `{kind, ref, label?}` · `GET /items/{id}/refs` · `DELETE /refs/{id}` — code grounding (`commit`|`file`|`pr`|`branch`|`url`)
- items accept `idempotency_key` (safe-retry create) and `acceptance_criteria` (`[{text, done}]`, definition-of-done); activity accepts `confidence` (`unspecified`|`inferred`|`confirmed`) and a `rejected` kind for dead-end approaches
- `GET/POST /webhooks`, `DELETE /webhooks/{id}` — event subscribers (`{project?, url, events?, secret?}`)
- `GET /search?q=` — cascading recall over items + activity: Postgres full-text → semantic (pgvector cosine) → trigram fuzzy
- `POST /ingest/bug` — `{site, url, message, severity?, meta?}` (ingest scope; CORS-enabled and per-IP rate-limited for cross-site widget use)

## MCP (`/mcp`, streamable-HTTP)

Wire into a Claude Code project's `.mcp.json` (needs a write-capable key):

```json
{
  "mcpServers": {
    "flightdeck": {
      "type": "http",
      "url": "http://100.79.61.79:4300/mcp",
      "headers": { "X-API-Key": "fd_your_agent_key" }
    }
  }
}
```

Tools — orient: `list_projects`, `get_project_context`, `get_global_context`,
`search`, `list_items`, `get_item`, `next_action`, `digest`, `stale`. Log:
`create_project`, `create_item`, `update_item`, `log_activity`,
`update_project_summary`, `set_project_instructions`, `link_items`,
`unlink_items`, `add_item_ref`, `list_item_refs`, `record_context_impact`.

`get_project_context` surfaces `rejected_approaches` (dead-end/out-of-scope
notes) and freshness (`activities_since_summary`) so an agent can judge whether
to trust the summary; items carry `acceptance_criteria` + `acceptance_unmet` (the
definition-of-done contract). `create_item`'s `idempotency_key` makes creation
safe to retry in autonomous loops; `log_activity`'s `confidence` distinguishes
human-confirmed truth from agent-inferred claims.

## Context effectiveness analytics

`record_context_impact` and `POST /api/context-impact` record one reported
effect of retrieved context in a caller-defined work session. The allowed
effect/mechanism pairs are:

- `helpful`: `decision_changed`, `prevented_error`,
  `duplicate_work_avoided`, or `reconstruction_saved`
- `neutral`: `ignored`
- `harmful`: `stale_or_incorrect`

Each report includes evidence and may include the relevant project item,
context references, a signed estimate of minutes saved or lost, and an
idempotency key. Identical retries return the original event; changed input
under an existing key returns a conflict. Impact events stay outside the
project activity feed so measurement does not become context noise. Raw
reports are available through `GET /api/context-impact`.

The existing REST and MCP usage report includes a `context_effectiveness`
section with contribution, prevented-error, duplicate-work-avoidance, harm,
and estimated-time measures across distinct `(actor, session_id)` pairs. Its
`measurement_basis` is always `reported_impacts`: these values
describe agent reports, not a proven causal effect. A controlled retrospective
or randomized evaluation is still required to establish what would have
happened without Flightdeck context.

`next_action` ranks open, unblocked items (dependency-aware via `link_items`
`blocks` edges); `digest` rolls up recent activity since a timestamp; `stale`
surfaces housekeeping targets for a scheduled agent. Webhooks (managed over the
HTTP API) POST a JSON envelope on `item.created`, `item.status_changed`, and
`activity.logged`, signed with `X-Flightdeck-Signature: sha256=…` when a secret
is set.

## Bug widget

Embed on any public site (uses an ingest-only key):

```html
<script src="http://100.79.61.79:4300/bug-widget.js"
        data-flightdeck-url="http://100.79.61.79:4300"
        data-site="finforge"
        data-key="fd_your_ingest_key" defer></script>
```

## Semantic search

`search` cascades full-text → **semantic** (meaning-based) → trigram. The
semantic tier embeds items with OpenAI `text-embedding-3-small` (1536-dim,
pgvector HNSW cosine index) so conceptually-related items surface even with no
shared keywords.

- Requires the `vector` extension — the shared Postgres runs the drop-in
  `pgvector/pgvector:pg17` image.
- Set `OPENAI_API_KEY` (compose reads it from an untracked `.env`). Optional:
  `FLIGHTDECK_EMBED_MODEL` (default `text-embedding-3-small`),
  `FLIGHTDECK_SEMANTIC_MAX_DISTANCE` (default `0.6`).
- **If `OPENAI_API_KEY` is unset, the embedder no-ops and search degrades
  cleanly to lexical-only (FTS + trigram)** — nothing breaks.
- A background worker backfills missing embeddings (on create, and after a
  title/body edit invalidates them); writes never block on the embedding API.

## Develop

```bash
# backend (needs DATABASE_URL to the shared Postgres on localhost:5432)
export DATABASE_URL="postgres://postgres:postgres@localhost:5432/flightdeck"
go run ./cmd/flightdeck serve            # :8080

# regenerate type-safe queries after editing internal/store/queries/*.sql
sqlc generate

# CI (.github/workflows/ci.yml) runs go build/vet/test and the web build on push/PR.

# frontend (proxies /api to :8080)
cd web && npm install && npm run dev
```

## Layout

```
cmd/flightdeck/main.go   serve | migrate | keygen; wires /api, /mcp, embedded UI
internal/store/          sqlc-generated queries + pgx pool + goose runner
internal/service/        shared logic (item create → activity, status_change log)
internal/api/            HTTP handlers
internal/mcp/            MCP SDK tool registrations
internal/auth/           X-API-Key middleware + scopes
internal/dto/            compact agent-friendly JSON shapes (shared api+mcp)
internal/embed/          OpenAI embeddings client (semantic search)
internal/pgvec/          NULL-aware pgvector.Vector wrapper
migrations/              goose SQL (embedded)
web/                     React + Vite + TS kanban (built → go:embed dist)
bug-widget.js            embeddable reporter (also served at /bug-widget.js)
```
