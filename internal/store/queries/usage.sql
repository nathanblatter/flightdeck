-- name: InsertToolCall :exec
INSERT INTO tool_calls (tool, actor, project, ok, error, duration_ms, args, result_bytes)
VALUES ($1, $2, $3, $4, $5, $6, COALESCE(sqlc.narg('args')::jsonb, '{}'), $7);

-- name: ToolCallStats :many
-- Per-tool behavior over a window: volume, error count, latency percentiles,
-- and average result size (the token-cost proxy agents pay to call it).
SELECT tool,
       count(*)                                                            AS calls,
       count(*) FILTER (WHERE NOT ok)                                      AS errors,
       (percentile_cont(0.5)  WITHIN GROUP (ORDER BY duration_ms))::float8 AS p50_ms,
       (percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_ms))::float8 AS p95_ms,
       COALESCE(avg(result_bytes), 0)::float8                              AS avg_result_bytes,
       max(called_at)::timestamptz                                         AS last_used
FROM tool_calls
WHERE called_at >= $1
GROUP BY tool
ORDER BY calls DESC;

-- name: DailyToolCalls :many
SELECT date_trunc('day', called_at)::timestamptz AS day,
       count(*)                         AS calls,
       count(*) FILTER (WHERE NOT ok)   AS errors
FROM tool_calls
WHERE called_at >= $1
GROUP BY 1
ORDER BY 1;

-- name: TopProjectsByToolCalls :many
SELECT project, count(*) AS calls
FROM tool_calls
WHERE called_at >= $1 AND project <> ''
GROUP BY project
ORDER BY calls DESC
LIMIT 10;

-- name: RecentToolErrors :many
SELECT tool, error, called_at
FROM tool_calls
WHERE called_at >= $1 AND NOT ok
ORDER BY called_at DESC
LIMIT 10;

-- name: PurgeOldToolCalls :execrows
DELETE FROM tool_calls WHERE called_at < $1;

-- name: InsertSearchLog :exec
INSERT INTO search_log (actor, query, fts_hits, semantic_hits, trigram_hits, activity_hits, returned)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: SearchUsageSummary :one
-- semantic_rescues / trigram_rescues: searches where lexical FTS found nothing
-- but a fallback tier did — direct evidence those tiers earn their keep.
SELECT count(*)                                                                          AS searches,
       count(*) FILTER (WHERE returned = 0 AND activity_hits = 0)                        AS zero_result,
       count(*) FILTER (WHERE fts_hits = 0 AND semantic_hits > 0)                        AS semantic_rescues,
       count(*) FILTER (WHERE fts_hits = 0 AND semantic_hits = 0 AND trigram_hits > 0)   AS trigram_rescues,
       COALESCE(avg(returned), 0)::float8                                                AS avg_returned
FROM search_log
WHERE searched_at >= $1;

-- name: RecentZeroResultSearches :many
SELECT query, searched_at
FROM search_log
WHERE searched_at >= $1 AND returned = 0 AND activity_hits = 0
ORDER BY searched_at DESC
LIMIT 10;

-- name: PurgeOldSearchLog :execrows
DELETE FROM search_log WHERE searched_at < $1;

-- name: EmbeddingCoverage :one
-- Semantic-tier backfill health: how many live items are embedded vs poison
-- ('failed'), and the same for high-signal activity (the kinds the embedder
-- targets). A low embedded fraction means semantic search is starved — no amount
-- of distance-threshold tuning helps until the backfill catches up.
SELECT
  (SELECT count(*) FROM items WHERE deleted_at IS NULL)                                                       AS items_total,
  (SELECT count(*) FROM items WHERE deleted_at IS NULL AND embedding IS NOT NULL)                             AS items_embedded,
  (SELECT count(*) FROM items WHERE deleted_at IS NULL AND embedding IS NULL AND embedding_model = 'failed')  AS items_failed,
  (SELECT count(*) FROM activity WHERE kind IN ('decision','progress','rejected') AND body <> '')             AS activity_total,
  (SELECT count(*) FROM activity a JOIN activity_embeddings e ON e.activity_id = a.id
     WHERE a.kind IN ('decision','progress','rejected') AND a.body <> '' AND e.embedding IS NOT NULL)         AS activity_embedded;
