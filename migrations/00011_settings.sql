-- +goose Up
-- +goose StatementBegin

-- Instance-level settings for self-contained deployments (work-instance
-- distribution). Holds what the first-run setup wizard configures: instance
-- name, optional OpenAI key (env var still wins when set), feature flags, and
-- the setup_complete marker. One row per key, jsonb value.
CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Quick-capture items (POST /api/ingest/capture — Apple Shortcuts et al) carry
-- their own source so agents can triage "things filed from a meeting" apart
-- from manual/agent/widget-created items.
ALTER TABLE items DROP CONSTRAINT items_source_check;
ALTER TABLE items ADD CONSTRAINT items_source_check
    CHECK (source IN ('manual', 'agent', 'bug_reporter', 'api', 'capture'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE items DROP CONSTRAINT items_source_check;
ALTER TABLE items ADD CONSTRAINT items_source_check
    CHECK (source IN ('manual', 'agent', 'bug_reporter', 'api'));
DROP TABLE IF EXISTS settings;
-- +goose StatementEnd
