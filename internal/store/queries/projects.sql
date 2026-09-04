-- name: GetProjectBySlug :one
SELECT * FROM projects WHERE slug = $1;

-- name: GetProjectByID :one
SELECT * FROM projects WHERE id = $1;

-- name: ListProjects :many
SELECT * FROM projects
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
ORDER BY
    CASE status WHEN 'active' THEN 0 WHEN 'paused' THEN 1 WHEN 'done' THEN 2 ELSE 3 END,
    updated_at DESC;

-- name: CreateProject :one
INSERT INTO projects (slug, name, status, summary, instructions, repo_url, site_url, aliases, parent_slug)
VALUES (
    $1,
    $2,
    COALESCE(sqlc.narg('status')::text, 'active'),
    COALESCE(sqlc.narg('summary')::text, ''),
    COALESCE(sqlc.narg('instructions')::text, ''),
    sqlc.narg('repo_url'),
    sqlc.narg('site_url'),
    COALESCE(sqlc.narg('aliases')::text[], '{}'),
    sqlc.narg('parent_slug')
)
RETURNING *;

-- name: UpdateProject :one
-- parent_slug needs a tri-state (leave / set / clear) that COALESCE can't
-- express, so set_parent gates the change and parent_slug carries set-vs-clear.
UPDATE projects SET
    name         = COALESCE(sqlc.narg('name')::text, name),
    status       = COALESCE(sqlc.narg('status')::text, status),
    summary      = COALESCE(sqlc.narg('summary')::text, summary),
    instructions = COALESCE(sqlc.narg('instructions')::text, instructions),
    repo_url     = COALESCE(sqlc.narg('repo_url')::text, repo_url),
    site_url     = COALESCE(sqlc.narg('site_url')::text, site_url),
    aliases      = COALESCE(sqlc.narg('aliases')::text[], aliases),
    parent_slug  = CASE WHEN sqlc.arg('set_parent')::bool
                        THEN sqlc.narg('parent_slug')::text
                        ELSE parent_slug END,
    updated_at   = now()
WHERE slug = sqlc.arg('slug')
RETURNING *;

-- name: ProjectDescendants :many
-- Slugs of the subtree rooted at $1, root included. UNION (not UNION ALL)
-- deduplicates, so this terminates even if a concurrent parent change ever
-- raced a cycle past validation.
WITH RECURSIVE subtree (slug) AS (
    SELECT root.slug FROM projects root WHERE root.slug = $1
    UNION
    SELECT p.slug FROM projects p JOIN subtree s ON p.parent_slug = s.slug
)
SELECT s.slug FROM subtree s;

-- name: ListChildProjects :many
SELECT slug, name, status, summary FROM projects
WHERE parent_slug = $1
ORDER BY name;

-- name: UpdateProjectSummary :one
UPDATE projects SET summary = $2, updated_at = now()
WHERE slug = $1
RETURNING *;

-- name: SetProjectInstructions :one
UPDATE projects SET instructions = $2, updated_at = now()
WHERE slug = $1
RETURNING *;

-- name: CountItemsByStatus :many
SELECT project_id, status, count(*) AS n
FROM items
WHERE deleted_at IS NULL
GROUP BY project_id, status;

-- name: CountItemsByStatusForProject :many
-- Single-project status counts — avoids the all-projects full scan when serving
-- a single-project orient.
SELECT status, count(*) AS n
FROM items
WHERE project_id = $1 AND deleted_at IS NULL
GROUP BY status;
