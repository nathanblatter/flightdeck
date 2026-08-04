-- +goose Up
-- +goose StatementBegin

-- Screenshot / file attachments on items (flightdeck-50). Blob bytes live in
-- object storage (MinIO via the S3 API); this table is the metadata + the
-- object key. Rows cascade with their item; the maintenance sweep deletes the
-- orphaned objects (the store row is authoritative).
CREATE TABLE attachments (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    item_id      uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    filename     text NOT NULL,
    content_type text NOT NULL,
    size_bytes   bigint NOT NULL,
    object_key   text NOT NULL UNIQUE,
    actor        text NOT NULL DEFAULT 'unknown',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX attachments_item_id_idx ON attachments (item_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS attachments;
-- +goose StatementEnd
