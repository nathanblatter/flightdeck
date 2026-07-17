-- +goose Up
-- +goose StatementBegin

-- Maintain updated_at automatically so no query has to remember to set it.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER projects_set_updated_at
    BEFORE UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER items_set_updated_at
    BEFORE UPDATE ON items
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS items_set_updated_at ON items;
DROP TRIGGER IF EXISTS projects_set_updated_at ON projects;
DROP FUNCTION IF EXISTS set_updated_at();
-- +goose StatementEnd
