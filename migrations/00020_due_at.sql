-- +goose Up
-- +goose StatementBegin

-- (flightdeck-92) Items get an optional due date. Life projects are date-driven
-- (flights, application windows, odometer-triggered maintenance, expiring
-- offers), and without a date nothing can answer "what is due this week" or
-- notice an item that quietly expired in todo. Partial index: the agenda only
-- ever scans dated, live rows.
ALTER TABLE items ADD COLUMN due_at timestamptz;
CREATE INDEX items_due_idx ON items (due_at) WHERE due_at IS NOT NULL AND deleted_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS items_due_idx;
ALTER TABLE items DROP COLUMN IF EXISTS due_at;
-- +goose StatementEnd
