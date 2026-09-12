-- name: GetProjectBySlug :one
SELECT * FROM projects WHERE slug = $1;

-- name: GetProjectByID :one
SELECT * FROM projects WHERE id = $1;

-- name: ListProjects :many
-- Archived projects are excluded unless asked for by name (status='archived').
-- That exclusion is what makes archiving mean something: without it 'archived'
-- is just a label and the project still clutters every orient read and picker.
SELECT * FROM projects
WHERE (sqlc.narg('status')::text IS NULL OR status = sqlc.narg('status'))
  AND (sqlc.narg('status')::text IS NOT NULL OR status <> 'archived')
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

-- name: DeleteProject :one
-- Hard-delete a project. items/activity/webhooks cascade; child projects are
-- re-rooted by the parent_slug ON DELETE SET NULL (the service refuses the
-- delete when children exist, so that path is a backstop, not the contract).
DELETE FROM projects WHERE slug = $1 RETURNING *;

-- name: ListAttachmentKeysForProject :many
-- Object keys owned by a project's items, collected before a purge so the
-- blobs can be removed — the attachment rows cascade away with the items and
-- would otherwise leave the objects orphaned in S3/MinIO forever.
SELECT a.object_key FROM attachments a
JOIN items i ON i.id = a.item_id
WHERE i.project_id = $1;

-- name: CountProjectContents :one
-- Pre-purge tally so the API can report (and the UI can confirm) exactly how
-- much is about to be destroyed. Counts soft-deleted items too: a hard delete
-- takes them as well.
SELECT
    (SELECT count(*) FROM items    i WHERE i.project_id = $1) AS items,
    (SELECT count(*) FROM activity a WHERE a.project_id = $1) AS activity;
