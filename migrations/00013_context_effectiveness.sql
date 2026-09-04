-- +goose Up
-- +goose StatementBegin

-- Agent-reported context outcomes live outside project activity so measurement
-- does not pollute the durable context agents read on future sessions.
CREATE TABLE context_impact_events (
    id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    recorded_at              timestamptz NOT NULL DEFAULT now(),
    actor                    text NOT NULL
                             CHECK (char_length(btrim(actor)) > 0),
    session_id               text NOT NULL
                             CHECK (char_length(btrim(session_id)) BETWEEN 1 AND 200),
    project                  text NOT NULL
                             CHECK (char_length(btrim(project)) > 0),
    item                     text,
    effect                   text NOT NULL
                             CHECK (effect IN ('helpful', 'neutral', 'harmful')),
    mechanism                text NOT NULL
                             CHECK (mechanism IN (
                                 'decision_changed',
                                 'prevented_error',
                                 'duplicate_work_avoided',
                                 'reconstruction_saved',
                                 'ignored',
                                 'stale_or_incorrect'
                             )),
    context_refs             text[] NOT NULL DEFAULT '{}'
                             CHECK (
                                 cardinality(context_refs) <= 20
                                 AND array_position(context_refs, NULL) IS NULL
                             ),
    evidence                 text NOT NULL
                             CHECK (char_length(btrim(evidence)) BETWEEN 1 AND 2000),
    estimated_minutes_delta  integer
                             CHECK (estimated_minutes_delta BETWEEN -1440 AND 1440),
    idempotency_key          text
                             CHECK (idempotency_key IS NULL OR char_length(idempotency_key) <= 200),
    request_fingerprint      text NOT NULL
                             CHECK (char_length(request_fingerprint) = 64),
    CHECK (
        (effect = 'helpful' AND mechanism IN (
            'decision_changed',
            'prevented_error',
            'duplicate_work_avoided',
            'reconstruction_saved'
        ))
        OR (effect = 'neutral' AND mechanism = 'ignored')
        OR (effect = 'harmful' AND mechanism = 'stale_or_incorrect')
    ),
    CHECK (
        estimated_minutes_delta IS NULL
        OR (effect = 'helpful' AND estimated_minutes_delta >= 0)
        OR (effect = 'neutral' AND estimated_minutes_delta = 0)
        OR (effect = 'harmful' AND estimated_minutes_delta <= 0)
    )
);

CREATE INDEX context_impact_recorded_idx ON context_impact_events (recorded_at DESC);
CREATE INDEX context_impact_project_idx ON context_impact_events (project, recorded_at DESC);
CREATE INDEX context_impact_session_idx ON context_impact_events (actor, session_id);
CREATE UNIQUE INDEX context_impact_idempotency_idx
    ON context_impact_events (actor, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS context_impact_events;
-- +goose StatementEnd
