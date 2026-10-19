-- +goose Up
-- +goose StatementBegin

-- (flightdeck-75) Telemetry for the any-term FTS tier and the semantic
-- ceiling. fts_any_hits: rows the coverage-floored OR tier contributed after
-- AND-FTS came up thin. best_semantic_distance: nearest cosine distance seen
-- for the query whether or not it cleared the ceiling — the evidence for
-- tuning FLIGHTDECK_SEMANTIC_MAX_DISTANCE (NULL when no embed ran).
ALTER TABLE search_log ADD COLUMN fts_any_hits int NOT NULL DEFAULT 0;
ALTER TABLE search_log ADD COLUMN best_semantic_distance float8;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE search_log DROP COLUMN IF EXISTS best_semantic_distance;
ALTER TABLE search_log DROP COLUMN IF EXISTS fts_any_hits;
-- +goose StatementEnd
