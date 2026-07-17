-- name: CreateItemLink :one
INSERT INTO item_links (from_item_id, to_item_id, kind)
VALUES ($1, $2, COALESCE(sqlc.narg('kind')::text, 'relates_to'))
ON CONFLICT (from_item_id, to_item_id, kind) DO UPDATE SET kind = EXCLUDED.kind
RETURNING *;

-- name: DeleteItemLink :exec
DELETE FROM item_links WHERE id = $1;

-- name: ListLinksForItem :many
SELECT * FROM item_links
WHERE from_item_id = $1 OR to_item_id = $1
ORDER BY created_at;

-- name: ListBlockingEdgesByProject :many
-- For a project, the active "blocks" edges: each row means blocked_id is blocked
-- by blocker_id (whose status is still open). Used to flag non-ready open items.
SELECT l.to_item_id AS blocked_id,
       l.from_item_id AS blocker_id,
       b.title AS blocker_title,
       b.status AS blocker_status
FROM item_links l
JOIN items blocked ON blocked.id = l.to_item_id
    AND blocked.project_id = $1 AND blocked.deleted_at IS NULL
JOIN items b ON b.id = l.from_item_id AND b.deleted_at IS NULL
WHERE l.kind = 'blocks' AND b.status NOT IN ('done', 'wontfix');

-- name: ListOpenBlockingEdges :many
-- Every active "blocks" edge across all projects: blocked_id is blocked by an
-- open blocker. Used to annotate /items list responses with blocked flags.
SELECT l.to_item_id AS blocked_id,
       l.from_item_id AS blocker_id,
       b.title AS blocker_title
FROM item_links l
JOIN items blocked ON blocked.id = l.to_item_id AND blocked.deleted_at IS NULL
JOIN items b ON b.id = l.from_item_id AND b.deleted_at IS NULL
WHERE l.kind = 'blocks' AND b.status NOT IN ('done', 'wontfix');

-- name: ListReadyItems :many
-- Open items across active projects that are NOT blocked by any open blocker,
-- ranked for "what should I work on": priority, then in_progress/todo, then age.
SELECT i.*, p.slug AS project_slug
FROM items i
JOIN projects p ON p.id = i.project_id AND p.status = 'active'
WHERE i.deleted_at IS NULL
  AND i.status NOT IN ('done', 'wontfix')
  AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
  AND NOT EXISTS (
      SELECT 1 FROM item_links l
      JOIN items b ON b.id = l.from_item_id AND b.deleted_at IS NULL
      WHERE l.kind = 'blocks' AND l.to_item_id = i.id
        AND b.status NOT IN ('done', 'wontfix')
  )
ORDER BY
    CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'med' THEN 2 ELSE 3 END,
    CASE i.status WHEN 'in_progress' THEN 0 WHEN 'todo' THEN 1 WHEN 'backlog' THEN 2 ELSE 3 END,
    i.updated_at ASC
LIMIT COALESCE(sqlc.narg('lim')::int, 20);
