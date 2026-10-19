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

-- name: SearchItemsAny :many
-- Any-term lexical tier with a coverage floor. plainto_tsquery ANDs every
-- term, so a natural-language agent query ("macbook laptop setup notes")
-- misses an item that carries most of those words. This tier ORs the query's
-- lexemes and keeps rows matching at least min_coverage of them and at least
-- min_matched lexemes — the floor is what stops never-filed topics from
-- returning confident noise on a single shared word. Ranked by coverage, then
-- ts_rank. Lexemes come pre-normalized from to_tsvector, so each is cast to a
-- tsquery verbatim (quoted) rather than re-stemmed.
WITH ql AS (
    SELECT DISTINCT lexeme FROM unnest(tsvector_to_array(to_tsvector('english', sqlc.arg('q')::text))) AS lexeme
), n AS (
    SELECT count(*)::int AS total FROM ql
), anyq AS (
    SELECT string_agg(format('%L', lexeme), ' | ')::tsquery AS q FROM ql
), hits AS (
    SELECT i.id,
           (SELECT count(*) FROM ql WHERE i.search @@ format('%L', ql.lexeme)::tsquery)::int AS matched,
           ts_rank(i.search, anyq.q) AS rank
    FROM items i, anyq
    WHERE i.deleted_at IS NULL
      AND i.search @@ anyq.q
      AND (sqlc.narg('project_id')::uuid IS NULL OR i.project_id = sqlc.narg('project_id'))
      AND (sqlc.narg('type')::text IS NULL OR i.type = sqlc.narg('type'))
)
SELECT i.*, h.matched, n.total, (h.matched::float8 / n.total)::float8 AS coverage, h.rank
FROM hits h
JOIN items i ON i.id = h.id
CROSS JOIN n
WHERE n.total >= 2
  AND h.matched >= sqlc.arg('min_matched')::int
  AND h.matched::float8 / n.total >= sqlc.arg('min_coverage')::float8
ORDER BY coverage DESC, h.rank DESC
LIMIT COALESCE(sqlc.narg('lim')::int, 50);

-- name: CountQueryLexemes :one
-- How many distinct lexemes English FTS extracts from a query (stop words
-- and punctuation removed). Decides whether the any-term tier can differ from
-- AND-FTS at all (needs >= 2) and gates the trigram fallback.
SELECT count(DISTINCT lexeme)::int AS n FROM unnest(tsvector_to_array(to_tsvector('english', sqlc.arg('q')::text))) AS lexeme;
