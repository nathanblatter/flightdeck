-- +goose Up
-- +goose StatementBegin

-- Project-scoped API keys. project_ids restricts a key to the listed projects;
-- NULL means unrestricted, which every pre-existing key stays.
--
-- IDs rather than slugs: a slug can be renamed (the FK on parent_slug cascades
-- renames, so they do happen) and a deleted project's slug can be reused by a
-- new project. Pinning the id means a rename keeps the key working and a
-- delete-and-recreate does NOT silently hand the key the new project — a
-- restricted key whose project is gone can write nowhere (fails closed). The
-- CLI still takes slugs (or aliases) and resolves them to ids once, at
-- keygen / restrict time. No FK is possible on an array element, so a deleted
-- project's id simply stays in the array and never matches again.
ALTER TABLE api_keys ADD COLUMN project_ids uuid[];

-- An empty restriction would be ambiguous next to NULL (unrestricted), so it
-- is not representable: restricted means at least one project.
ALTER TABLE api_keys ADD CONSTRAINT api_keys_project_ids_nonempty
    CHECK (project_ids IS NULL OR cardinality(project_ids) > 0);

-- Project limits are enforced only on the ingest surface; read/write routes
-- and /mcp refuse a restricted key outright (see auth.Middleware). A
-- restricted key with read or write scope would therefore be misleading, so
-- the database refuses it too, not just the CLI.
ALTER TABLE api_keys ADD CONSTRAINT api_keys_restricted_ingest_only
    CHECK (project_ids IS NULL OR NOT (scopes && ARRAY['read', 'write']::text[]));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_restricted_ingest_only;
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_project_ids_nonempty;
ALTER TABLE api_keys DROP COLUMN IF EXISTS project_ids;
-- +goose StatementEnd
