-- +goose Up
-- +goose StatementBegin

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- projects: one row per tracked project; summary is the agent-maintained orient payload.
CREATE TABLE projects (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       text NOT NULL UNIQUE,
    name       text NOT NULL,
    status     text NOT NULL DEFAULT 'active'
               CHECK (status IN ('active', 'paused', 'done', 'archived')),
    summary    text NOT NULL DEFAULT '',
    repo_url   text,
    site_url   text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- items: the kanban cards — tasks/bugs/ideas/notes.
CREATE TABLE items (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id   uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type         text NOT NULL DEFAULT 'task'
                 CHECK (type IN ('task', 'bug', 'idea', 'note')),
    title        text NOT NULL,
    body         text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'backlog'
                 CHECK (status IN ('backlog', 'todo', 'in_progress', 'blocked', 'done', 'wontfix')),
    priority     text NOT NULL DEFAULT 'med'
                 CHECK (priority IN ('low', 'med', 'high', 'urgent')),
    assignee     text,
    position     double precision NOT NULL DEFAULT 0,
    source       text NOT NULL DEFAULT 'manual'
                 CHECK (source IN ('manual', 'agent', 'bug_reporter', 'api')),
    external_ref text,
    tags         text[] NOT NULL DEFAULT '{}',
    metadata     jsonb NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    closed_at    timestamptz,
    deleted_at   timestamptz,
    search       tsvector GENERATED ALWAYS AS (
                     setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
                     setweight(to_tsvector('english', coalesce(body, '')), 'B')
                 ) STORED
);

CREATE INDEX items_project_idx  ON items (project_id);
CREATE INDEX items_status_idx   ON items (status);
CREATE INDEX items_type_idx     ON items (type);
CREATE INDEX items_assignee_idx ON items (assignee);
CREATE INDEX items_updated_idx  ON items (updated_at DESC);
CREATE INDEX items_tags_idx     ON items USING gin (tags);
CREATE INDEX items_search_idx   ON items USING gin (search);

-- activity: append-only log capturing the "why" — decisions, progress, status changes.
CREATE TABLE activity (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    item_id    uuid REFERENCES items(id) ON DELETE SET NULL,
    kind       text NOT NULL DEFAULT 'comment'
               CHECK (kind IN ('decision', 'progress', 'status_change', 'comment', 'created')),
    actor      text NOT NULL DEFAULT '',
    body       text NOT NULL DEFAULT '',
    metadata   jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    search     tsvector GENERATED ALWAYS AS (
                   to_tsvector('english', coalesce(body, ''))
               ) STORED
);

CREATE INDEX activity_project_idx ON activity (project_id);
CREATE INDEX activity_item_idx    ON activity (item_id);
CREATE INDEX activity_kind_idx    ON activity (kind);
CREATE INDEX activity_created_idx ON activity (created_at DESC);
CREATE INDEX activity_search_idx  ON activity USING gin (search);

-- api_keys: hashed static keys with scopes (read/write/ingest).
CREATE TABLE api_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL,
    key_hash     text NOT NULL UNIQUE,
    scopes       text[] NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked      boolean NOT NULL DEFAULT false
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS activity;
DROP TABLE IF EXISTS items;
DROP TABLE IF EXISTS projects;
-- +goose StatementEnd
