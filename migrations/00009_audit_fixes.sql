-- +goose Up
-- +goose StatementBegin

-- (flightdeck-33) Idempotency keys are a per-project contract: two projects
-- reusing the same key (agents naturally pick keys like "fix-login-bug") must
-- not collide. The old global unique index let create_item return another
-- project's item.
DROP INDEX IF EXISTS items_idempotency_key_idx;
CREATE UNIQUE INDEX items_idempotency_key_idx ON items (project_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- (flightdeck-35) Per-hook delivery tracking + a distinct parked state.
-- delivered_hook_ids accumulates the subscribers that already ACKed, so a retry
-- only re-POSTs to the ones that failed. parked_at marks a dead-lettered event
-- (attempts exhausted) instead of overloading delivered_at.
ALTER TABLE webhook_events ADD COLUMN delivered_hook_ids uuid[] NOT NULL DEFAULT '{}';
ALTER TABLE webhook_events ADD COLUMN parked_at timestamptz;

DROP INDEX IF EXISTS webhook_events_pending_idx;
CREATE INDEX webhook_events_pending_idx ON webhook_events (next_attempt_at)
    WHERE delivered_at IS NULL AND parked_at IS NULL;

-- (flightdeck-38) Semantic search over the activity log. Decisions are the
-- highest-value content in flightdeck, and they were only lexically searchable.
-- A side table keeps the hot activity SELECTs (orient, feeds) free of 1536-dim
-- vectors; rows are joined in only by the embedder and semantic search.
-- model='failed' marks a poison row the embedder should stop retrying.
CREATE TABLE activity_embeddings (
    activity_id uuid PRIMARY KEY REFERENCES activity(id) ON DELETE CASCADE,
    embedding   vector(1536),
    model       text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX activity_embeddings_hnsw_idx ON activity_embeddings
    USING hnsw (embedding vector_cosine_ops);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS activity_embeddings;
DROP INDEX IF EXISTS webhook_events_pending_idx;
CREATE INDEX webhook_events_pending_idx ON webhook_events (next_attempt_at)
    WHERE delivered_at IS NULL;
ALTER TABLE webhook_events DROP COLUMN IF EXISTS parked_at;
ALTER TABLE webhook_events DROP COLUMN IF EXISTS delivered_hook_ids;
DROP INDEX IF EXISTS items_idempotency_key_idx;
CREATE UNIQUE INDEX items_idempotency_key_idx ON items (idempotency_key)
    WHERE idempotency_key IS NOT NULL;
-- +goose StatementEnd
