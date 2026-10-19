-- +goose Up
-- +goose StatementBegin

-- (flightdeck-74) Session identity on usage telemetry. Without it "agents
-- rarely orient before writing" is inferred from aggregate ratios; with it a
-- session can be classified (oriented first / wrote without orienting /
-- reported impact). The MCP transport assigns the id (Mcp-Session-Id); REST
-- callers record '' because they have no session.
ALTER TABLE tool_calls ADD COLUMN session_id text NOT NULL DEFAULT '';
ALTER TABLE search_log ADD COLUMN session_id text NOT NULL DEFAULT '';

CREATE INDEX tool_calls_session_idx ON tool_calls (session_id, called_at)
    WHERE session_id <> '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS tool_calls_session_idx;
ALTER TABLE search_log DROP COLUMN IF EXISTS session_id;
ALTER TABLE tool_calls DROP COLUMN IF EXISTS session_id;
-- +goose StatementEnd
