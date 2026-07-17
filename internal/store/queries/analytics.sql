-- name: ActivityKindCountsSince :many
SELECT kind, count(*) AS n
FROM activity
WHERE project_id = $1 AND created_at >= $2
GROUP BY kind;

-- name: CountDistinctItemsTouchedSince :one
SELECT count(DISTINCT item_id) AS n
FROM activity
WHERE project_id = $1 AND created_at >= $2 AND item_id IS NOT NULL;

-- name: ListStaleInProgress :many
-- in_progress items whose last update is older than the cutoff.
SELECT i.*, p.slug AS project_slug
FROM items i
JOIN projects p ON p.id = i.project_id AND p.status = 'active'
WHERE i.deleted_at IS NULL AND i.status = 'in_progress' AND i.updated_at < $1
ORDER BY i.updated_at ASC;

-- name: ListUntriagedBugs :many
-- bugs still sitting in backlog (never triaged) older than the cutoff.
SELECT i.*, p.slug AS project_slug
FROM items i
JOIN projects p ON p.id = i.project_id AND p.status = 'active'
WHERE i.deleted_at IS NULL AND i.type = 'bug' AND i.status = 'backlog' AND i.created_at < $1
ORDER BY i.created_at ASC;

-- name: ListStaleProjectSummaries :many
-- active projects whose latest activity is newer than the last summary refresh.
SELECT p.*, max(a.created_at)::timestamptz AS last_activity
FROM projects p
JOIN activity a ON a.project_id = p.id
WHERE p.status = 'active'
GROUP BY p.id
HAVING max(a.created_at) > p.updated_at + interval '1 hour'
ORDER BY max(a.created_at) DESC;
