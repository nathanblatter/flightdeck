-- +goose Up
-- +goose StatementBegin

-- Projects form a tree via parent_slug (adjacency list). Slug is the natural
-- key everywhere in the API, so the FK targets it directly: renames cascade,
-- deleting a parent orphans children to roots instead of deleting them.
-- Cycle prevention lives in the service (ProjectDescendants walk before any
-- parent change); the CHECK below only stops the trivial self-parent.
ALTER TABLE projects
    ADD COLUMN parent_slug text
    REFERENCES projects (slug) ON UPDATE CASCADE ON DELETE SET NULL,
    ADD CONSTRAINT projects_no_self_parent CHECK (parent_slug IS DISTINCT FROM slug);

CREATE INDEX projects_parent_idx ON projects (parent_slug) WHERE parent_slug IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE projects
    DROP CONSTRAINT projects_no_self_parent,
    DROP COLUMN parent_slug;
-- +goose StatementEnd
