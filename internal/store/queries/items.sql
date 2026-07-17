-- name: GetItem :one
SELECT * FROM items WHERE id = $1 AND deleted_at IS NULL;

-- name: GetItemByRef :one
SELECT * FROM items WHERE lower(ref) = lower($1) AND deleted_at IS NULL;

-- name: ListItems :many
SELECT * FROM items
WHERE deleted_at IS NULL
  AND (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('type')::text IS NULL OR type = sqlc.narg('type'))
  AND (sqlc.narg('assignee')::text IS NULL OR assignee = sqlc.narg('assignee'))
  AND (sqlc.narg('tag')::text IS NULL OR sqlc.narg('tag')::text = ANY(tags))
  AND (sqlc.narg('updated_since')::timestamptz IS NULL OR updated_at >= sqlc.narg('updated_since'))
  AND (sqlc.narg('q')::text IS NULL OR search @@ plainto_tsquery('english', sqlc.narg('q')))
ORDER BY position ASC, created_at DESC
LIMIT COALESCE(sqlc.narg('lim')::int, 500)
OFFSET COALESCE(sqlc.narg('off')::int, 0);

-- name: ListOpenItemsByProject :many
SELECT * FROM items
WHERE project_id = $1 AND deleted_at IS NULL AND status NOT IN ('done', 'wontfix')
ORDER BY
    CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'med' THEN 2 ELSE 3 END,
    position ASC
LIMIT $2;

-- name: CreateItem :one
INSERT INTO items (project_id, type, title, body, status, priority, assignee, position, source, external_ref, tags, metadata, idempotency_key, acceptance_criteria)
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
    COALESCE(sqlc.narg('acceptance_criteria')::jsonb, '[]')
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
