-- +goose Up
-- +goose StatementBegin

-- (flightdeck-93) `reference` item type: durable facts about a project (specs,
-- account/vehicle details, dossiers) that are not work. The CHECK was declared
-- unnamed in 00001_init.sql, so Postgres auto-named it items_type_check.
ALTER TABLE items DROP CONSTRAINT items_type_check;
ALTER TABLE items ADD CONSTRAINT items_type_check
    CHECK (type IN ('task', 'bug', 'idea', 'note', 'reference'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE items SET type = 'note' WHERE type = 'reference';
ALTER TABLE items DROP CONSTRAINT items_type_check;
ALTER TABLE items ADD CONSTRAINT items_type_check
    CHECK (type IN ('task', 'bug', 'idea', 'note'));
-- +goose StatementEnd
