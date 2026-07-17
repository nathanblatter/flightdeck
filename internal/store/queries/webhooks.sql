-- name: CreateWebhook :one
INSERT INTO webhooks (project_id, url, secret, events)
VALUES (
    sqlc.narg('project_id'),
    $1,
    COALESCE(sqlc.narg('secret')::text, ''),
    COALESCE(sqlc.narg('events')::text[], '{}')
)
RETURNING *;

-- name: ListWebhooks :many
SELECT * FROM webhooks ORDER BY created_at DESC;

-- name: ListActiveWebhooksForEvent :many
-- Webhooks subscribed to this project (or all projects) and this event (or all).
SELECT * FROM webhooks
WHERE active = true
  AND (project_id IS NULL OR project_id = sqlc.arg('project_id'))
  AND (cardinality(events) = 0 OR sqlc.arg('event')::text = ANY(events))
ORDER BY created_at;

-- name: DeleteWebhook :exec
DELETE FROM webhooks WHERE id = $1;

-- --- durable outbox ---

-- name: EnqueueWebhookEvent :one
-- Called inside the originating write's transaction (transactional outbox).
INSERT INTO webhook_events (project_id, event, payload)
VALUES (sqlc.narg('project_id'), $1, $2)
RETURNING *;

-- name: LeaseWebhookEvents :many
-- Atomically claim a batch of due events and push their next attempt into the
-- future (a lease), so delivery happens outside the row lock and concurrent
-- workers never grab the same event.
UPDATE webhook_events SET next_attempt_at = now() + interval '1 minute'
WHERE id IN (
    SELECT id FROM webhook_events
    WHERE delivered_at IS NULL AND parked_at IS NULL AND next_attempt_at <= now()
    ORDER BY next_attempt_at
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg('lim')::int
)
RETURNING *;

-- name: MarkWebhookEventDelivered :exec
UPDATE webhook_events SET delivered_at = now(), last_error = $2 WHERE id = $1;

-- name: RescheduleWebhookEvent :exec
-- delivered_hook_ids records the subscribers that already ACKed this event, so
-- the retry only re-POSTs to the ones still failing (no duplicate deliveries).
UPDATE webhook_events
SET attempts = attempts + 1, next_attempt_at = $2, last_error = $3,
    delivered_hook_ids = $4
WHERE id = $1;

-- name: ParkWebhookEvent :exec
-- Dead-letter: attempts exhausted. Distinct from delivered_at so the two states
-- can't be conflated; the row stays visible for the operator dead-letter view.
UPDATE webhook_events
SET parked_at = now(), last_error = $2, delivered_hook_ids = $3
WHERE id = $1;

-- name: PurgeDeliveredWebhookEvents :execrows
DELETE FROM webhook_events WHERE delivered_at IS NOT NULL AND delivered_at < $1;

-- name: PurgeParkedWebhookEvents :execrows
DELETE FROM webhook_events WHERE parked_at IS NOT NULL AND parked_at < $1;

-- name: ListFailedWebhookEvents :many
-- Dead-lettered / erroring events for operator visibility (last_error set).
SELECT * FROM webhook_events
WHERE last_error <> ''
ORDER BY created_at DESC
LIMIT $1;
