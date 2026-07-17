-- +goose Up
-- +goose StatementBegin

-- (26) Optimistic concurrency: a monotonic version per item. update_item bumps
-- it and can be guarded by an expected_version (compare-and-swap), so two agents
-- editing the same item get a conflict instead of silently clobbering.
ALTER TABLE items ADD COLUMN version int NOT NULL DEFAULT 1;

-- (29) Key expiry: an optional expiry so keys can be time-boxed / rotated.
ALTER TABLE api_keys ADD COLUMN expires_at timestamptz;

-- (28) Fuzzy recall: pg_trgm enables typo/fragment matching (refs, code symbols)
-- that English FTS misses. Trigram GIN indexes back similarity + ILIKE search.
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX items_title_trgm_idx ON items USING gin (title gin_trgm_ops);
CREATE INDEX items_ref_trgm_idx   ON items USING gin (ref gin_trgm_ops);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS items_ref_trgm_idx;
DROP INDEX IF EXISTS items_title_trgm_idx;
ALTER TABLE api_keys DROP COLUMN IF EXISTS expires_at;
ALTER TABLE items DROP COLUMN IF EXISTS version;
-- +goose StatementEnd
