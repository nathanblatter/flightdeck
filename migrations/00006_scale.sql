-- +goose Up
-- +goose StatementBegin

-- Durable webhook outbox: events are enqueued in the SAME transaction as the
-- originating write (transactional outbox), then a background worker leases and
-- delivers them with retry/backoff — so a subscriber being down or a process
-- restart no longer drops events (at-least-once delivery).
CREATE TABLE webhook_events (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id      uuid REFERENCES projects(id) ON DELETE CASCADE,
    event           text NOT NULL,
    payload         jsonb NOT NULL,
    attempts        int NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    delivered_at    timestamptz,
    last_error      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- Partial index over just the undelivered backlog — the worker's hot path.
CREATE INDEX webhook_events_pending_idx ON webhook_events (next_attempt_at)
    WHERE delivered_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS webhook_events;
-- +goose StatementEnd
