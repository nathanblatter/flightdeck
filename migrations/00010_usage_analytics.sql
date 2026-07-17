-- +goose Up
-- +goose StatementBegin

-- (flightdeck-40) Usage analytics: how agents actually use flightdeck.
-- /metrics has aggregate RED numbers; these tables keep enough per-call detail
-- to answer behavioral questions — which tools go unused, which searches come
-- back empty, how big responses are (token cost) — so the service can be tuned
-- from data instead of guesses.

-- One row per MCP tools/call. args is the raw argument object (capped at write
-- time), result_bytes the marshaled result size — the token-cost proxy.
CREATE TABLE tool_calls (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    called_at    timestamptz NOT NULL DEFAULT now(),
    tool         text NOT NULL,
    actor        text NOT NULL DEFAULT '',
    project      text NOT NULL DEFAULT '', -- slug when the call names one
    ok           boolean NOT NULL,
    error        text NOT NULL DEFAULT '',
    duration_ms  int NOT NULL DEFAULT 0,
    args         jsonb NOT NULL DEFAULT '{}',
    result_bytes int NOT NULL DEFAULT 0
);

CREATE INDEX tool_calls_called_idx ON tool_calls (called_at DESC);
CREATE INDEX tool_calls_tool_idx   ON tool_calls (tool);

-- One row per search (MCP or REST), with per-tier hit counts so zero-result
-- queries and semantic/trigram "rescues" of lexical misses are measurable.
CREATE TABLE search_log (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    searched_at   timestamptz NOT NULL DEFAULT now(),
    actor         text NOT NULL DEFAULT '',
    query         text NOT NULL,
    fts_hits      int NOT NULL DEFAULT 0,
    semantic_hits int NOT NULL DEFAULT 0,
    trigram_hits  int NOT NULL DEFAULT 0,
    activity_hits int NOT NULL DEFAULT 0,
    returned      int NOT NULL DEFAULT 0 -- items ultimately returned
);

CREATE INDEX search_log_searched_idx ON search_log (searched_at DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS search_log;
DROP TABLE IF EXISTS tool_calls;
-- +goose StatementEnd
