-- name: SearchItems :many
SELECT i.*, ts_rank(i.search, plainto_tsquery('english', sqlc.arg('q'))) AS rank
FROM items i
WHERE i.deleted_at IS NULL
  AND i.search @@ plainto_tsquery('english', sqlc.arg('q'))
  AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('type')::text IS NULL OR i.type = sqlc.narg('type'))
ORDER BY rank DESC
LIMIT COALESCE(sqlc.narg('lim')::int, 50);

-- name: SearchItemsFuzzy :many
-- Trigram (pg_trgm) fuzzy match for typos / partial refs / code fragments that
-- English FTS misses. Ranked by similarity to title or ref.
SELECT i.*, greatest(similarity(i.title, sqlc.arg('q')), similarity(i.ref, sqlc.arg('q'))) AS sim
FROM items i
WHERE i.deleted_at IS NULL
  AND (i.title % sqlc.arg('q') OR i.ref % sqlc.arg('q') OR i.title ILIKE '%' || sqlc.arg('q') || '%')
  AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
  AND (sqlc.narg('type')::text IS NULL OR i.type = sqlc.narg('type'))
ORDER BY sim DESC
LIMIT COALESCE(sqlc.narg('lim')::int, 25);

-- name: ListTopOpenItemsPerProject :many
-- Top-N open items for every active project in a single windowed query — replaces
-- the per-project N+1 loop that built the global "load the map" view.
SELECT * FROM (
    SELECT i.*, p.slug AS project_slug,
           row_number() OVER (
               PARTITION BY i.project_id
               ORDER BY CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'med' THEN 2 ELSE 3 END,
                        i.position ASC
           ) AS rn
    FROM items i
    JOIN projects p ON p.id = i.project_id AND p.status = 'active'
    WHERE i.deleted_at IS NULL AND i.status NOT IN ('done', 'wontfix')
) s
WHERE s.rn <= sqlc.arg('per_project')::int
ORDER BY s.project_slug, s.rn;
