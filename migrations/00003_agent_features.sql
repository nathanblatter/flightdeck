-- +goose Up
-- +goose StatementBegin

-- Project-specific conventions an agent reads on orient (e.g. "run tests before
-- marking done"). Centralizes what otherwise lives scattered in CLAUDE.md files.
ALTER TABLE projects ADD COLUMN instructions text NOT NULL DEFAULT '';

-- item_links: directed relationships between items.
--   blocks     -> from_item blocks to_item (to_item is not "ready" until from closes)
--   relates_to -> soft association
--   parent_of  -> from_item is the parent/epic of to_item (subtask)
CREATE TABLE item_links (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    from_item_id uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    to_item_id   uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    kind         text NOT NULL DEFAULT 'relates_to'
                 CHECK (kind IN ('blocks', 'relates_to', 'parent_of')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (from_item_id <> to_item_id),
    UNIQUE (from_item_id, to_item_id, kind)
);
CREATE INDEX item_links_from_idx ON item_links (from_item_id);
CREATE INDEX item_links_to_idx   ON item_links (to_item_id);

-- webhooks: external subscribers notified on events (best-effort, async POST with
-- an HMAC-SHA256 signature). A null project_id subscribes to all projects; an
-- empty events array subscribes to all event kinds.
CREATE TABLE webhooks (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid REFERENCES projects(id) ON DELETE CASCADE,
    url        text NOT NULL,
    secret     text NOT NULL DEFAULT '',
    events     text[] NOT NULL DEFAULT '{}',
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhooks_project_idx ON webhooks (project_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS webhooks;
DROP TABLE IF EXISTS item_links;
ALTER TABLE projects DROP COLUMN IF EXISTS instructions;
-- +goose StatementEnd
