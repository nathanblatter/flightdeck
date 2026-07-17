-- name: CreateItemRef :one
INSERT INTO item_refs (item_id, kind, ref, label)
VALUES (
    $1,
    COALESCE(sqlc.narg('kind')::text, 'url'),
    sqlc.arg('ref'),
    COALESCE(sqlc.narg('label')::text, '')
)
RETURNING *;

-- name: ListItemRefs :many
SELECT * FROM item_refs WHERE item_id = $1 ORDER BY created_at;

-- name: DeleteItemRef :exec
DELETE FROM item_refs WHERE id = $1;

-- name: GetItemByIdempotencyKey :one
-- Scoped to the project: idempotency keys are only unique per project, so a
-- cross-project key reuse must not return another project's item.
SELECT * FROM items
WHERE project_id = $1 AND idempotency_key = $2 AND deleted_at IS NULL;

-- name: ListRejectedByProject :many
SELECT * FROM activity
WHERE project_id = $1 AND kind = 'rejected'
ORDER BY created_at DESC
LIMIT $2;

-- name: CountActivitySince :one
SELECT count(*) FROM activity WHERE project_id = $1 AND created_at > $2;