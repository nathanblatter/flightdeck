-- name: CreateContextImpactEvent :one
INSERT INTO context_impact_events (
    actor,
    session_id,
    project,
    item,
    effect,
    mechanism,
    context_refs,
    evidence,
    estimated_minutes_delta,
    idempotency_key
)
VALUES (
    sqlc.arg('actor'),
    sqlc.arg('session_id'),
    sqlc.arg('project'),
    sqlc.narg('item'),
    sqlc.arg('effect'),
    sqlc.arg('mechanism'),
    sqlc.arg('context_refs'),
    sqlc.arg('evidence'),
    sqlc.narg('estimated_minutes_delta'),
    sqlc.narg('idempotency_key')
)
RETURNING *;

-- name: GetContextImpactByIdempotencyKey :one
SELECT *
FROM context_impact_events
WHERE actor = sqlc.arg('actor')
  AND idempotency_key = sqlc.arg('idempotency_key');

-- name: ListContextImpactEvents :many
SELECT *
FROM context_impact_events
WHERE recorded_at >= sqlc.arg('recorded_at')
  AND (sqlc.narg('project')::text IS NULL OR project = sqlc.narg('project'))
ORDER BY recorded_at DESC
LIMIT sqlc.arg('lim');

-- name: ContextEffectivenessSummary :one
WITH sessions AS (
    SELECT
        actor,
        session_id,
        bool_or(effect = 'helpful') AS helpful,
        bool_or(effect = 'neutral') AS neutral,
        bool_or(effect = 'harmful') AS harmful,
        bool_or(mechanism = 'prevented_error') AS prevented_error,
        bool_or(mechanism = 'duplicate_work_avoided') AS duplicate_work_avoided,
        sum(COALESCE(estimated_minutes_delta, 0)) AS estimated_minutes_net
    FROM context_impact_events
    WHERE recorded_at >= sqlc.arg('recorded_at')
    GROUP BY actor, session_id
)
SELECT
    count(*) AS reported_sessions,
    count(*) FILTER (WHERE helpful) AS helpful_sessions,
    count(*) FILTER (WHERE neutral) AS neutral_sessions,
    count(*) FILTER (WHERE harmful) AS harmful_sessions,
    count(*) FILTER (WHERE prevented_error) AS prevented_error_sessions,
    count(*) FILTER (WHERE duplicate_work_avoided) AS duplicate_work_avoided_sessions,
    COALESCE(sum(estimated_minutes_net), 0)::bigint AS estimated_minutes_net
FROM sessions;

