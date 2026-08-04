-- name: CreateAttachment :one
INSERT INTO attachments (item_id, filename, content_type, size_bytes, object_key, actor)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetAttachment :one
SELECT * FROM attachments WHERE id = $1;

-- name: ListAttachmentsForItem :many
SELECT * FROM attachments WHERE item_id = $1 ORDER BY created_at;

-- name: CountAttachmentsForItem :one
SELECT count(*) FROM attachments WHERE item_id = $1;

-- name: DeleteAttachment :one
DELETE FROM attachments WHERE id = $1 RETURNING *;
