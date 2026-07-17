-- +goose Up
-- +goose StatementBegin

-- (1) Short item handles: a gapless per-project sequence gives every item a
-- human/agent-friendly ref like "finforge-42" alongside its UUID. The ref is
-- denormalized onto the row (computed from the project slug at insert time) so
-- every existing SELECT picks it up for free and lookups are a single index hit.
ALTER TABLE projects ADD COLUMN item_seq bigint NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN seq bigint;
ALTER TABLE items ADD COLUMN ref text;

-- assign_item_ref atomically bumps the owning project's counter (row lock
-- serializes concurrent inserts on the same project) and stamps seq + ref.
CREATE OR REPLACE FUNCTION assign_item_ref() RETURNS trigger AS $$
DECLARE
    s text;
    n bigint;
BEGIN
    UPDATE projects SET item_seq = item_seq + 1
        WHERE id = NEW.project_id
        RETURNING item_seq, slug INTO n, s;
    NEW.seq := n;
    NEW.ref := s || '-' || n;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER items_assign_ref
    BEFORE INSERT ON items
    FOR EACH ROW WHEN (NEW.seq IS NULL)
    EXECUTE FUNCTION assign_item_ref();

-- Backfill: number existing items per project by creation order, stamp refs,
-- then advance each project's counter past its current max.
WITH numbered AS (
    SELECT id,
           row_number() OVER (PARTITION BY project_id ORDER BY created_at, id) AS rn
    FROM items
)
UPDATE items i SET seq = n.rn FROM numbered n WHERE i.id = n.id;

UPDATE items i SET ref = p.slug || '-' || i.seq
FROM projects p WHERE p.id = i.project_id;

UPDATE projects p SET item_seq = sub.maxseq
FROM (SELECT project_id, max(seq) AS maxseq FROM items GROUP BY project_id) sub
WHERE p.id = sub.project_id;

ALTER TABLE items ALTER COLUMN seq SET NOT NULL;
ALTER TABLE items ALTER COLUMN ref SET NOT NULL;
CREATE UNIQUE INDEX items_ref_idx ON items (ref);
CREATE UNIQUE INDEX items_project_seq_idx ON items (project_id, seq);

-- (2) Project resolution from where an agent is standing: aliases lets a project
-- be found by repo dir name / path token (e.g. "Survivor50Draft" -> survivor50)
-- so the agent passes its cwd instead of consulting a hand-kept slug table.
ALTER TABLE projects ADD COLUMN aliases text[] NOT NULL DEFAULT '{}';

-- Seed aliases for the known projects whose repo dir differs from the slug
-- (matching is case-insensitive; only the differing names need seeding).
UPDATE projects SET aliases = '{Ai-therapist}'     WHERE slug = 'ai-therapist';
UPDATE projects SET aliases = '{Personal-Site}'     WHERE slug = 'personal-site';
UPDATE projects SET aliases = '{Survivor50Draft}'   WHERE slug = 'survivor50';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE projects DROP COLUMN IF EXISTS aliases;
DROP INDEX IF EXISTS items_project_seq_idx;
DROP INDEX IF EXISTS items_ref_idx;
DROP TRIGGER IF EXISTS items_assign_ref ON items;
DROP FUNCTION IF EXISTS assign_item_ref();
ALTER TABLE items DROP COLUMN IF EXISTS ref;
ALTER TABLE items DROP COLUMN IF EXISTS seq;
ALTER TABLE projects DROP COLUMN IF EXISTS item_seq;
-- +goose StatementEnd
