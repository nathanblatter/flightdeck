-- name: ListActivity :many
SELECT * FROM activity
WHERE (sqlc.narg('project_id')::uuid IS NULL OR project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('item_id')::uuid IS NULL OR item_id = sqlc.narg('item_id'))
  AND (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind'))
  AND (sqlc.narg('since')::timestamptz IS NULL OR created_at >= sqlc.narg('since'))
ORDER BY created_at DESC
LIMIT COALESCE(sqlc.narg('lim')::int, 200);

-- name: ListRecentActivityByProject :many
SELECT * FROM activity
WHERE project_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: ListRecentDecisionsByProject :many
SELECT * FROM activity
WHERE project_id = $1 AND kind IN ('decision', 'status_change')
ORDER BY created_at DESC
LIMIT $2;

-- name: CreateActivity :one
INSERT INTO activity (project_id, item_id, kind, actor, body, metadata, confidence)
VALUES (
    $1,
    sqlc.narg('item_id'),
    COALESCE(sqlc.narg('kind')::text, 'comment'),
    COALESCE(sqlc.narg('actor')::text, ''),
    COALESCE(sqlc.narg('body')::text, ''),
    COALESCE(sqlc.narg('metadata')::jsonb, '{}'),
    COALESCE(sqlc.narg('confidence')::text, 'unspecified')
)
RETURNING *;

-- name: SearchActivity :many
SELECT a.*, ts_rank(a.search, plainto_tsquery('english', sqlc.arg('q'))) AS rank
FROM activity a
WHERE a.search @@ plainto_tsquery('english', sqlc.arg('q'))
  AND (sqlc.narg('project_id')::uuid IS NULL OR a.project_id = sqlc.narg('project_id'))
ORDER BY rank DESC
LIMIT COALESCE(sqlc.narg('lim')::int, 50);
