-- name: GetItem :one
SELECT * FROM items WHERE id = $1 AND deleted_at IS NULL;

-- name: GetItemByRef :one
SELECT * FROM items WHERE lower(ref) = lower($1) AND deleted_at IS NULL;

-- name: ListItems :many
-- Items of archived projects are hidden unless that project is asked for by id.
-- Archiving is meant to declutter; leaving its items on the board (with no
-- resolvable project chip, since the project is gone from the listing) would
-- defeat the point.
SELECT i.* FROM items i
WHERE i.deleted_at IS NULL
  AND (sqlc.narg('project_id')::uuid IS NOT NULL OR NOT EXISTS (
        SELECT 1 FROM projects p
        WHERE p.id = i.project_id AND p.status = 'archived'))
  AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('status')::text IS NULL OR i.status = sqlc.narg('status'))
  AND (sqlc.narg('type')::text IS NULL OR i.type = sqlc.narg('type'))
  AND (sqlc.narg('assignee')::text IS NULL OR i.assignee = sqlc.narg('assignee'))
  AND (sqlc.narg('tag')::text IS NULL OR sqlc.narg('tag')::text = ANY(i.tags))
  AND (sqlc.narg('updated_since')::timestamptz IS NULL OR i.updated_at >= sqlc.narg('updated_since'))
  AND (sqlc.narg('q')::text IS NULL OR i.search @@ plainto_tsquery('english', sqlc.narg('q')))
  AND (sqlc.narg('due_before')::timestamptz IS NULL OR (i.due_at IS NOT NULL AND i.due_at <= sqlc.narg('due_before')))
ORDER BY i.position ASC, i.created_at DESC
LIMIT COALESCE(sqlc.narg('lim')::int, 500)
OFFSET COALESCE(sqlc.narg('off')::int, 0);

-- name: ListOpenItemsByProject :many
-- Reference items are facts, not work — they live in their own orient block
-- (ListReferenceItemsByProject), never in open_items.
SELECT * FROM items
WHERE project_id = $1 AND deleted_at IS NULL AND status NOT IN ('done', 'wontfix')
  AND type <> 'reference'
ORDER BY
    CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'med' THEN 2 ELSE 3 END,
    position ASC
LIMIT $2;

-- name: ListReferenceItemsByProject :many
-- The project's durable facts (type=reference) for the orient bundle, in the
-- order the kanban shows them. Status is ignored: a fact is a fact wherever
-- its card sits.
SELECT * FROM items
WHERE project_id = $1 AND deleted_at IS NULL AND type = 'reference'
ORDER BY position ASC, title ASC
LIMIT $2;

-- name: CreateItem :one
INSERT INTO items (project_id, type, title, body, status, priority, assignee, position, source, external_ref, tags, metadata, idempotency_key, acceptance_criteria, due_at)
VALUES (
    $1,
    COALESCE(sqlc.narg('type')::text, 'task'),
    sqlc.arg('title'),
    COALESCE(sqlc.narg('body')::text, ''),
    COALESCE(sqlc.narg('status')::text, 'backlog'),
    COALESCE(sqlc.narg('priority')::text, 'med'),
    sqlc.narg('assignee'),
    COALESCE(sqlc.narg('position')::double precision, 0),
    COALESCE(sqlc.narg('source')::text, 'manual'),
    sqlc.narg('external_ref'),
    COALESCE(sqlc.narg('tags')::text[], '{}'),
    COALESCE(sqlc.narg('metadata')::jsonb, '{}'),
    sqlc.narg('idempotency_key'),
    COALESCE(sqlc.narg('acceptance_criteria')::jsonb, '[]'),
    sqlc.narg('due_at')::timestamptz
)
RETURNING *;

-- name: UpdateItem :one
-- Bumps version on every write. When expected_version is supplied it acts as a
-- compare-and-swap: a mismatch matches no row (caller maps that to a conflict).
UPDATE items SET
    title     = COALESCE(sqlc.narg('title')::text, title),
    body      = COALESCE(sqlc.narg('body')::text, body),
    type      = COALESCE(sqlc.narg('type')::text, type),
    status    = COALESCE(sqlc.narg('status')::text, status),
    priority  = COALESCE(sqlc.narg('priority')::text, priority),
    assignee  = COALESCE(sqlc.narg('assignee')::text, assignee),
    position  = COALESCE(sqlc.narg('position')::double precision, position),
    tags      = COALESCE(sqlc.narg('tags')::text[], tags),
    metadata  = COALESCE(sqlc.narg('metadata')::jsonb, metadata),
    acceptance_criteria = COALESCE(sqlc.narg('acceptance_criteria')::jsonb, acceptance_criteria),
    -- due_at: clear_due_at wipes it, a supplied value replaces it, absent keeps it.
    due_at    = CASE
                    WHEN sqlc.arg('clear_due_at')::bool THEN NULL
                    ELSE COALESCE(sqlc.narg('due_at')::timestamptz, due_at)
                END,
    closed_at = CASE
                    WHEN COALESCE(sqlc.narg('status')::text, status) IN ('done', 'wontfix')
                        THEN COALESCE(closed_at, now())
                    ELSE NULL
                END,
    -- Invalidate the embedding when the searchable content changes; the
    -- background embedder will re-embed rows whose embedding is NULL. The model
    -- marker is reset too so a previously 'failed' (poison) row gets retried
    -- with its new content.
    embedding = CASE
                    WHEN sqlc.narg('title')::text IS NOT NULL OR sqlc.narg('body')::text IS NOT NULL
                        THEN NULL
                    ELSE embedding
                END,
    embedding_model = CASE
                    WHEN sqlc.narg('title')::text IS NOT NULL OR sqlc.narg('body')::text IS NOT NULL
                        THEN ''
                    ELSE embedding_model
                END,
    version    = version + 1,
    updated_at = now()
WHERE id = sqlc.arg('id') AND deleted_at IS NULL
  AND (sqlc.narg('expected_version')::int IS NULL OR version = sqlc.narg('expected_version'))
RETURNING *;

-- name: SoftDeleteItem :one
UPDATE items SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING *;

-- name: ListAgendaItems :many
-- Dated open items across active projects, due on or before the horizon,
-- oldest due first — so overdue rows lead. last_activity is the newest activity
-- row on the item (its creation when the log was trimmed): an overdue item
-- with no activity since its due date has quietly expired, and the agenda
-- flags it.
SELECT i.*, p.slug AS project_slug,
       COALESCE((SELECT max(a.created_at) FROM activity a WHERE a.item_id = i.id), i.created_at)::timestamptz AS last_activity
FROM items i
JOIN projects p ON p.id = i.project_id AND p.status = 'active'
WHERE i.deleted_at IS NULL
  AND i.status NOT IN ('done', 'wontfix')
  AND i.due_at IS NOT NULL
  AND i.due_at <= sqlc.arg('horizon')::timestamptz
ORDER BY i.due_at ASC, CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'med' THEN 2 ELSE 3 END
LIMIT sqlc.arg('lim')::int;
