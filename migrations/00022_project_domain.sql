-- +goose Up
-- +goose StatementBegin

-- (flightdeck-96) Project domain and sensitivity. domain groups projects by
-- the part of life they belong to so an agent can ask for the work map (or
-- the life map) alone instead of one 50 KB global context. sensitivity=private
-- keeps a project out of every cross-project surface (global context,
-- list_projects, search, next_action, stale, usage top_projects) unless the
-- call names its slug explicitly.
ALTER TABLE projects ADD COLUMN domain text NOT NULL DEFAULT 'work'
    CHECK (domain IN ('life', 'school', 'work'));
ALTER TABLE projects ADD COLUMN sensitivity text NOT NULL DEFAULT 'normal'
    CHECK (sensitivity IN ('normal', 'private'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE projects DROP COLUMN IF EXISTS sensitivity;
ALTER TABLE projects DROP COLUMN IF EXISTS domain;
-- +goose StatementEnd
