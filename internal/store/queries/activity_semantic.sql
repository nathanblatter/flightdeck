-- name: ListActivityNeedingEmbedding :many
-- High-signal activity (decisions, progress, rejections — the "why" agents
-- search for) that has no embedding yet. Rows with a 'failed' marker are poison
-- and skipped. Activity is immutable, so there is no re-embed-on-edit path.
SELECT a.id, a.body
FROM activity a
LEFT JOIN activity_embeddings e ON e.activity_id = a.id
WHERE a.kind IN ('decision', 'progress', 'rejected')
  AND a.body <> ''
  AND e.activity_id IS NULL
ORDER BY a.created_at DESC
LIMIT sqlc.arg('lim')::int;

-- name: InsertActivityEmbedding :exec
-- Upsert so marking a row 'failed' and later embedding it (or vice versa in a
-- race) never errors.
INSERT INTO activity_embeddings (activity_id, embedding, model)
VALUES (sqlc.arg('activity_id'), sqlc.narg('embedding')::vector, sqlc.arg('model')::text)
ON CONFLICT (activity_id) DO UPDATE SET embedding = EXCLUDED.embedding, model = EXCLUDED.model;

-- name: SearchActivitySemantic :many
-- Approximate nearest-neighbour search over decision/progress bodies by cosine
-- distance, bounded by max_distance like the item variant.
SELECT a.id, a.project_id, a.item_id, a.kind, a.actor, a.body, a.metadata,
       a.confidence, a.created_at,
       (e.embedding <=> sqlc.arg('query_embedding')::vector) AS distance
FROM activity a
JOIN activity_embeddings e ON e.activity_id = a.id
WHERE e.embedding IS NOT NULL
  AND (sqlc.narg('project_id')::uuid IS NULL OR a.project_id = sqlc.narg('project_id'))
  AND (e.embedding <=> sqlc.arg('query_embedding')::vector) < sqlc.arg('max_distance')::float8
ORDER BY e.embedding <=> sqlc.arg('query_embedding')::vector
LIMIT COALESCE(sqlc.narg('lim')::int, 25);
