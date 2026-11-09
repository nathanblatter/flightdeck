-- +goose Up
-- +goose StatementBegin

-- (flightdeck-94) Recurring items. recur is either an ISO-8601 duration subset
-- (P<n>D / P<n>W / P<n>M / P<n>Y) that the service applies to due_at when the
-- item is completed, or free text an agent interprets ("at 72,900 mi", "every
-- even year in October"). Completing a recurring item spawns its successor with
-- the same rule; the rule itself is validated at the app layer (see
-- service.ParseRecur), so the column stays plain text.
ALTER TABLE items ADD COLUMN recur text;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE items DROP COLUMN IF EXISTS recur;
-- +goose StatementEnd
