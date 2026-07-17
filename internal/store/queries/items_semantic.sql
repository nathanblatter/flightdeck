-- name: ListItemsNeedingEmbedding :many
-- Live items whose embedding is missing (never embedded, or invalidated by a
-- content edit). The background embedder drains this in batches. Rows marked
-- embedding_model='failed' are poison (repeatedly rejected) and skipped so one
-- bad item can't stall the whole backlog; a content edit clears the marker.
SELECT id, ref, title, body
FROM items
WHERE deleted_at IS NULL AND embedding IS NULL AND embedding_model <> 'failed'
ORDER BY updated_at DESC
LIMIT sqlc.arg('lim')::int;

-- name: MarkItemEmbeddingFailed :exec
-- Park a poison item so the embedder stops retrying it. Guarded by the
-- still-NULL check so a successful concurrent embed isn't clobbered.
UPDATE items SET embedding_model = 'failed'
WHERE id = $1 AND embedding IS NULL;

-- name: SetItemEmbedding :exec
-- Store an embedding, but only if the item's content hasn't changed since it was
-- read for embedding (guarded by the still-NULL check) — so an edit that races
-- the embedder isn't clobbered with a stale vector.
UPDATE items
SET embedding = sqlc.arg('embedding')::vector,
    embedding_model = sqlc.arg('embedding_model')::text
WHERE id = sqlc.arg('id') AND embedding IS NULL;

-- name: SearchItemsSemantic :many
-- Approximate nearest-neighbour search by cosine distance. Only rows within
-- max_distance are returned, so an off-topic query yields nothing (and the
-- caller falls through to trigram) rather than surfacing the globally-closest
-- but irrelevant items.
SELECT i.*, (i.embedding <=> sqlc.arg('query_embedding')::vector) AS distance
FROM items i
WHERE i.deleted_at IS NULL
  AND i.embedding IS NOT NULL
  AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('type')::text IS NULL OR i.type = sqlc.narg('type'))
  AND (i.embedding <=> sqlc.arg('query_embedding')::vector) < sqlc.arg('max_distance')::float8
ORDER BY i.embedding <=> sqlc.arg('query_embedding')::vector
LIMIT COALESCE(sqlc.narg('lim')::int, 25);
