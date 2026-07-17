---
title: "Flightdeck: Giving My AI Agents a Memory"
description: "A self-hosted, agent-first context layer that lets Claude read project state before it acts and log what changed after — one Go binary, an MCP server, and a kanban board on a shared Postgres."
date: "2026-06-26"
readTime: "8 min read"
tags:
  - FLIGHTDECK
  - MCP
  - CLAUDE
  - AI-AGENTS
  - RAG
  - GO
  - POSTGRES
  - PGVECTOR
  - SQLC
  - SELF-HOSTED
  - HOMELAB
  - DOCKER
  - TAILSCALE
  - SIDE-PROJECT
cover: "/images/flightdeck-cover.png"
---

# Flightdeck: Giving My AI Agents a Memory

> A self-hosted, agent-first context layer — built so Claude can pick up any of my projects cold and already know what's going on.

## Why?

The short version: my AI agents had amnesia.

I run a fleet of projects — a finance app, a couple of party games, a personal
site, a few bots — and I lean hard on Claude Code to build them. But every coding
session starts from zero. The agent doesn't know what I decided last week, which
bugs are open, or *why* a thing is built the way it is. So I'd spend the first ten
minutes of every session re-explaining context I'd already explained five times.

The state of each project lived in my head and in scattered git history. Nothing
connected them. Nothing survived between sessions.

So I built the thing I actually wanted: **a shared memory the agents read before
they act, and write to after.** I call it Flightdeck.

> [!NOTE]
> This isn't a class project. It's infrastructure I use every single day — the
> context for *this very blog post* was pulled out of Flightdeck by an agent.

## What It Does

Flightdeck is one store of project state — **projects, work items, and an activity
log** — shaped for agents to follow a simple loop:

```
read context  →  do the work  →  log what changed
```

It speaks two protocols at once. To **me**, it's a kanban board in the browser. To
**my agents**, it's an [MCP](https://modelcontextprotocol.io) server — a set of
tools Claude can call directly.

```
        ┌─────────────────────────────────────────────────────────┐
        │  Claude Code sessions (one per project, all stateless)   │
        │                                                          │
        │   finforge      personal-site      impostor    natebot   │
        └───────┬──────────────┬───────────────┬───────────┬──────┘
                │              │               │           │
                │         MCP (read → act → log) over Tailscale
                │              │               │           │
                └──────────────┴───────┬───────┴───────────┘
                                       ▼
                          ┌─────────────────────────┐
                          │       FLIGHTDECK         │
                          │   one Go binary :4300    │
                          │                          │
                          │  /mcp   agent tools      │
                          │  /api   REST             │
                          │  /      embedded kanban  │
                          │  /metrics  Prometheus    │
                          └────────────┬─────────────┘
                                       ▼
                          ┌─────────────────────────┐
                          │  PostgreSQL 17 + pgvector │
                          │  projects · items · log   │
                          └─────────────────────────┘
```

The whole point is the **flywheel**: every time an agent does real work, it leaves
a trail — a closed bug, a status change, a logged decision with the *why*. The next
session (or a different agent, on a different project) reads that trail and is
instantly oriented. The context layer stays fresh as a *side effect* of doing the
work, not because I maintain it by hand.

## The Stack

One self-contained Go binary does almost everything. The React UI is compiled and
**embedded into the binary** with `go:embed`, so there's no separate frontend to
deploy — `flightdeck serve` is the whole app.

```ini
Frontend:    React + TypeScript (Vite) → go:embed into the binary
Backend:     Go (single static binary, no runtime deps)
Database:    PostgreSQL 17 + pgvector
Queries:     sqlc (compile-time type-safe SQL)
Migrations:  goose (embedded, run on startup)
DB driver:   pgx/v5 (tuned connection pool)
Agent API:   MCP — modelcontextprotocol/go-sdk
Embeddings:  OpenAI text-embedding-3-small (1536-dim)
Metrics:     hand-rolled Prometheus (HTTP + per-tool RED)
Access:      Tailscale-only (no public exposure)
Deploy:      Docker Compose on the homelab
```

I went with **Go** because I wanted a single binary I could drop into a container
with zero runtime dependencies. **sqlc** because I'd rather the compiler catch a
broken query than find out in prod — I write plain SQL, it generates type-safe Go.
**Postgres** because it does full-text search, JSON, *and* vector search without me
bolting on a second datastore.

## How It's Hosted

Flightdeck rides along on the same shared Postgres as the rest of my homelab,
deployed with one command from my `docker-services` compose stack:

```bash
docker compose up -d --build flightdeck
```

```yaml
services:
  postgres:
    # the official Postgres image + the vector extension (drop-in)
    image: pgvector/pgvector:pg17
  flightdeck:
    build: ./flightdeck          # multi-stage: node builds SPA → go embeds it
    ports:
      - "100.x.x.x:4300:8080"     # bound to the Tailscale IP only
    environment:
      DATABASE_URL: postgres://…/flightdeck
      OPENAI_API_KEY: ${OPENAI_API_KEY:-}   # optional — degrades gracefully
```

Migrations run automatically on container startup (goose, embedded), so a deploy
*is* a migration. A separate sidecar takes a nightly `pg_dump` — because if
Flightdeck **is** the memory, losing it means every project goes amnesiac again.

> [!TIP]
> Binding the port to the Tailscale IP instead of `0.0.0.0` means the service is
> reachable from all my devices and *nowhere else* — no firewall rules, no public
> surface, no auth headaches for the internal API.

## How an Agent Uses It

The agent contract is small on purpose. **Orient** at the start of a task:

```python
flightdeck.get_project_context("finforge")
# → summary, open items, recent decisions, "what to work on next"
```

**Log** as the work happens — this is the highest-value write, because code never
records the *why*:

```python
flightdeck.log_activity(
    project="finforge",
    kind="decision",
    body="Switched cash-flow forecast to a 60–90 day window; "
         "shorter windows were too noisy to act on.",
)
```

Items use short, human-friendly handles like `finforge-42` that work anywhere a
raw UUID would — so an agent (or I) can refer to work by name, not by GUID.

> [!IMPORTANT]
> Two agents can run against the same project at once. Every item carries a
> `version`, and updates use a compare-and-swap — a stale write gets a `409
> Conflict` instead of silently clobbering the other agent's change. Multi-agent
> safety was a requirement, not an afterthought.

## The Part I'm Proudest Of: Three-Tier Search

An agent asking *"what was that thing about stopping double-writes?"* won't use the
exact words I filed the item under. Keyword search falls on its face there. So
Flightdeck's search **cascades through three strategies**, precise to fuzzy:

```
full-text (Postgres FTS)
      │  no hits?
      ▼
semantic (pgvector cosine + OpenAI embeddings)   ← finds meaning, not words
      │  nothing close enough?
      ▼
trigram (pg_trgm)                                ← catches typos & fragments
```

The semantic tier is the fun one. A background worker embeds every item with
OpenAI's `text-embedding-3-small` and stores the vector in Postgres, indexed with
**HNSW** for fast approximate nearest-neighbour search:

```sql
CREATE EXTENSION IF NOT EXISTS vector;
ALTER TABLE items ADD COLUMN embedding vector(1536);
CREATE INDEX items_embedding_hnsw_idx
    ON items USING hnsw (embedding vector_cosine_ops);
```

Embeddings are produced **out-of-band** — creates and edits never block on the
OpenAI API. A new item is embedded within ~15s; editing its title or body nulls
the vector so the worker re-embeds it; and if no key is configured, the whole tier
quietly switches off and search stays lexical-only. Nothing breaks.

Here's it working across my *entire* corpus — one query, no project filter,
searching 95 items spanning 9 projects, ranked purely by meaning:

| I searched for…                                   | It found                                            |
| ------------------------------------------------- | --------------------------------------------------- |
| `prevent agents from overwriting each others changes` | **flightdeck-26** — *Optimistic concurrency (multi-agent CAS)* |
| `budgeting and financial planning calculations`   | **finforge-8** — *Cash-flow runway forecast*        |
| `nightly snapshot to survive a disk failure`      | **flightdeck-27** — *Automated off-box DB backups*  |

None of those queries share meaningful keywords with the item they matched. That's
the difference between *search* and *recall* — and it's what makes the memory
actually useful to an agent that phrases things its own way.

> [!TIP]
> pgvector lets you do RAG-style semantic retrieval without standing up a separate
> vector database. If you're already on Postgres, the `vector` extension is a
> shockingly small amount of new infrastructure for what you get.

## Progress

- [x] **Foundation** — Go binary, Postgres, sqlc + goose, embedded React kanban
- [x] **MCP server** — the agent tool surface (orient + log)
- [x] **Agent ergonomics** — short refs, `resolve_project`, `next_action`, `digest`
- [x] **Hardening** — scoped API keys, CORS, enum validation, graceful shutdown
- [x] **Scale** — cursor pagination, tuned pool, write-invalidated cache, `/metrics`
- [x] **Durability** — transactional webhook outbox + retry worker, nightly backups
- [x] **Concurrency** — optimistic-lock CAS so multi-agent writes don't clobber
- [x] **Integration tests** — DB-backed suite wired into CI via a Postgres service
- [x] **Semantic search** — pgvector + OpenAI, FTS → semantic → trigram cascade
- [ ] **Hybrid ranking** — blend lexical + semantic to sharpen cross-project recall
- [ ] **Off-site backup push** — get the dumps off the box entirely

## The Hardest Part

Making it genuinely *agent-first* instead of a human app an agent happens to call.

That sounds soft, but it's concrete: agents pay for context in tokens, so the API
has a **compact mode** that truncates item bodies and returns lightweight "what's
next" briefs by default, with full bodies only when the UI asks. The orient call
folds in everything needed to start work in *one* round-trip. Decisions get logged
with confidence levels so an agent can tell *"Nathan confirmed this"* from *"an
agent inferred this."* Every one of those was a deliberate design choice in service
of a reader that isn't human.

The runner-up was a nasty little bug: the Go pgvector library relies on
`database/sql` interfaces, but its scanner **errored on a NULL vector**. The moment
I added a nullable embedding column, every `SELECT *` in the app broke. The fix was
a thin NULL-aware wrapper — but finding it meant the integration suite earned its
keep that day.

## The Best Part

Opening a brand-new Claude Code session on a project I hadn't touched in two weeks,
typing nothing but *"what's the status?"*, and watching the agent call Flightdeck,
read its own trail of decisions, and tell **me** what was left to do.

The memory had outlived the conversation. The flywheel was turning on its own.
That's the moment it stopped being a project and became infrastructure.

## What "Done" Looks Like

Any agent, on any of my projects, oriented in one call — and leaving the context
sharper than it found it, without me asking. No cold starts. No re-explaining. Just
a layer of shared memory that quietly keeps itself current as the work gets done.

No social features, no dashboards-for-dashboards' sake. Just continuity.

---

*Built with **Go**, **PostgreSQL + pgvector**, **sqlc**, **MCP**, and the **OpenAI
embeddings API**. Self-hosted on the homelab, Tailscale-only, one binary, one
`docker compose up`.*
