-- +goose Up
-- +goose StatementBegin

-- (1) Negative knowledge: a 'rejected' activity kind for approaches tried and
-- abandoned / things explicitly out of scope — surfaced on orient so agents
-- don't confidently rebuild dead work.
ALTER TABLE activity DROP CONSTRAINT IF EXISTS activity_kind_check;
ALTER TABLE activity ADD CONSTRAINT activity_kind_check
    CHECK (kind IN ('decision', 'progress', 'status_change', 'comment', 'created', 'rejected'));

-- (2) Provenance / trust: distinguish human-confirmed ground truth from
-- agent-inferred claims so agents don't compound each other's errors.
ALTER TABLE activity ADD COLUMN confidence text NOT NULL DEFAULT 'unspecified'
    CHECK (confidence IN ('unspecified', 'inferred', 'confirmed'));

-- (3) Code grounding: connect an item ("what") to where it lives ("where") —
-- commits, files, PRs, branches, urls — so an agent jumps straight to the code.
CREATE TABLE item_refs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    item_id    uuid NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    kind       text NOT NULL DEFAULT 'url'
               CHECK (kind IN ('commit', 'file', 'pr', 'branch', 'url')),
    ref        text NOT NULL,
    label      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX item_refs_item_idx ON item_refs (item_id);

-- (4) Idempotency: a client-supplied key makes create-item safe to retry across
-- interruptions / compactions in autonomous loops, instead of duplicating.
ALTER TABLE items ADD COLUMN idempotency_key text;
CREATE UNIQUE INDEX items_idempotency_key_idx ON items (idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- (5) Definition of done: structured acceptance criteria (a checklist with
-- checked state) — the explicit contract an agent satisfies before 'done'.
ALTER TABLE items ADD COLUMN acceptance_criteria jsonb NOT NULL DEFAULT '[]';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE items DROP COLUMN IF EXISTS acceptance_criteria;
DROP INDEX IF EXISTS items_idempotency_key_idx;
ALTER TABLE items DROP COLUMN IF EXISTS idempotency_key;
DROP TABLE IF EXISTS item_refs;
ALTER TABLE activity DROP COLUMN IF EXISTS confidence;
ALTER TABLE activity DROP CONSTRAINT IF EXISTS activity_kind_check;
ALTER TABLE activity ADD CONSTRAINT activity_kind_check
    CHECK (kind IN ('decision', 'progress', 'status_change', 'comment', 'created'));
-- +goose StatementEnd
