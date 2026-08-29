# Running a private flightdeck instance (work laptop / new job)

A repeatable recipe for standing up a **self-contained, localhost-only flightdeck**
on any machine — its own Postgres, its own keys, zero overlap with the personal
instance. Total setup time: ~10 minutes.

## Prerequisites

- Docker Desktop (or any `docker compose`-capable runtime)
- Go (only to run `flightdeck up` from the repo; the app itself runs in Docker)
- This repo cloned somewhere (e.g. `~/dev/flightdeck`)

## 1. Bootstrap the instance

```sh
cd ~/dev/flightdeck
go run ./cmd/flightdeck up --dir ~/.flightdeck/work --port 4310
```

This creates `~/.flightdeck/work/` with:

- `.env` — generated secrets (Postgres password, one-time **setup token**). Written
  once, never overwritten; re-running `up` is safe.
- `docker-compose.yml` — pgvector Postgres + Redis + the flightdeck binary, port
  bound to **127.0.0.1 only** (work data never leaves the machine).
- `pgdata/`, `backups/`

…then runs `docker compose up -d --build` and prints the URL and setup token.

## 2. First-run wizard

Open http://127.0.0.1:4310 — the setup wizard appears (instead of the key gate):

1. Paste the **setup token** from the `up` output (or `~/.flightdeck/work/.env`).
2. Name the instance (e.g. "Flightdeck @ Lumion"), optionally add an OpenAI key
   (semantic search; skippable — search falls back to full-text), set flags.
3. Finish → three keys are minted and **shown once**:
   - `personal` (read,write) — auto-saved in the browser; the UI logs straight in.
   - `agent` (read,write) — for Claude Code / MCP.
   - `ingest` (ingest) — for the capture Shortcut; safe to embed.
4. The final screen has copy-paste snippets for everything below.

## 3. Wire up Claude Code (MCP)

In each work repo, `.mcp.json`:

```json
{
  "mcpServers": {
    "flightdeck": {
      "type": "http",
      "url": "http://127.0.0.1:4310/mcp",
      "headers": { "X-API-Key": "<agent key>" }
    }
  }
}
```

Create projects via MCP (`create_project`) or the API, then work as usual:
orient with `get_project_context`, log with `create_item` / `log_activity`.

## 4. Meeting quick-capture (Apple Shortcuts "desktop widget")

macOS widgets can't take text input, so capture is an Apple Shortcut run from
the desktop **Shortcuts widget** (one tap → prompt). Build it once in
Shortcuts.app (the wizard's final screen shows this pre-filled):

1. **Ask for Input** · Text · "Capture what?"
2. **Get Contents of URL** · GET `http://127.0.0.1:4310/api/ingest/projects` · header `X-API-Key: <ingest key>`
3. **Choose from List** · input: Contents of URL
4. **List** · `task`, `bug`, `idea`
5. **Choose from List** · input: List
6. **Get Contents of URL** · POST `http://127.0.0.1:4310/api/ingest/capture` · same header · JSON body:
   `project` = Chosen Item (3), `type` = Chosen Item (5), `title` = Provided Input (1)
   (send exactly these fields — unknown fields are rejected)
7. **Show Notification** · "Captured ✓"

Then right-click desktop → Edit Widgets → add the **Shortcuts** widget →
pick this Shortcut. Captured items land in the project backlog with
`source=capture`, ready for the next agent session to triage ("check the queue").

Scriptable capture works the same way:

```sh
curl -X POST http://127.0.0.1:4310/api/ingest/capture \
  -H "X-API-Key: <ingest key>" -H "Content-Type: application/json" \
  -d '{"project":"myproj","title":"do the thing","type":"task"}'
```

## Operations

- **Upgrade**: `go run ./cmd/flightdeck update --dir ~/.flightdeck/work --port 4310`
  (same flags as `up`) — fetches tags, checks out the latest release (`vX.Y.Z`),
  rebuilds, and waits for `/healthz`. `.env` and data are preserved. The running
  instance tells you when an update exists: MCP orient calls carry a notice line
  and the web UI shows a banner. Releases are cut by pushing a `v*` tag; the
  `release.yml` workflow publishes the GitHub Release once tests pass.
- **Backup**: `docker compose -p work --project-directory ~/.flightdeck/work exec postgres pg_dump -U postgres flightdeck > ~/.flightdeck/work/backups/$(date +%F).sql`
- **Settings later**: `GET/PUT /api/settings` (write key) — instance name,
  OpenAI key, flags. No restart needed.
- **More keys**: `docker compose -p work --project-directory ~/.flightdeck/work exec flightdeck flightdeck keygen <name> <scopes>`
- **Stop / teardown**: `docker compose -p work --project-directory ~/.flightdeck/work down` (add `-v` + delete the dir to erase everything).

## Notes / caveats

- The OpenAI key entered in the wizard is stored plaintext in the instance's
  own Postgres (localhost-only, single-user). Prefer the `OPENAI_API_KEY` env
  var in `.env` if that bothers you — env always wins over the DB setting.
- `flightdeck up` builds the image from the repo checkout; there is no
  published registry image.
- The setup wizard only ever appears on a fresh instance (no API keys). An
  existing deployment is treated as already set up.
