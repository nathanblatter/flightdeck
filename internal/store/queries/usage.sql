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

-- name: ActorStats :many
-- Per-actor behavior over a window. actor is the API key name (OAuth tokens
-- inherit it), so this is per-client/per-device usage: how much each caller
-- does, how often it orients before writing, what it searches, and whether
-- it errs. orient_calls counts the read-side entry points agents are meant
-- to start from.
SELECT actor,
       count(*)                                                            AS calls,
       count(*) FILTER (WHERE NOT ok)                                      AS errors,
       count(*) FILTER (WHERE tool IN ('get_project_context', 'get_global_context', 'list_projects', 'resolve_project')) AS orient_calls,
       count(*) FILTER (WHERE tool IN ('create_item', 'create_items', 'update_item', 'complete_item', 'log_activity',
                                       'update_project_summary', 'set_project_instructions', 'create_project',
                                       'archive_project', 'link_items', 'unlink_items', 'add_item_ref'))      AS write_calls,
       count(*) FILTER (WHERE tool = 'search')                             AS search_calls,
       (percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms))::float8  AS p50_ms,
       COALESCE(avg(result_bytes), 0)::float8                              AS avg_result_bytes,
       min(called_at)::timestamptz                                         AS first_used,
       max(called_at)::timestamptz                                         AS last_used,
       (SELECT array_agg(t ORDER BY c DESC, t)
          FROM (SELECT tool AS t, count(*) AS c FROM tool_calls i
                WHERE i.actor = o.actor AND i.called_at >= $1
                GROUP BY tool ORDER BY c DESC, tool LIMIT 5) top)::text[]  AS top_tools
FROM tool_calls o
WHERE called_at >= $1
GROUP BY actor
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
