-- name: GetAPIKeyByHash :one
SELECT * FROM api_keys
WHERE key_hash = $1 AND revoked = false
  AND (expires_at IS NULL OR expires_at > now());

-- name: GetAPIKeyByID :one
SELECT * FROM api_keys WHERE id = $1;

-- name: CreateAPIKey :one
-- project_ids NULL = unrestricted (see migration 00024).
INSERT INTO api_keys (name, key_hash, scopes, expires_at, project_ids)
VALUES ($1, $2, $3, sqlc.narg('expires_at'), sqlc.narg('project_ids')::uuid[])
RETURNING *;

-- name: SetAPIKeyProjects :one
-- Restrict (non-NULL) or unrestrict (NULL) an existing key without re-minting
-- its secret.
UPDATE api_keys SET project_ids = sqlc.narg('project_ids')::uuid[]
WHERE id = $1
RETURNING id, name, scopes, project_ids, created_at, last_used_at, revoked;

-- name: TouchAPIKey :exec
UPDATE api_keys SET last_used_at = now() WHERE id = $1;

-- name: RevokeAPIKey :one
UPDATE api_keys SET revoked = true WHERE id = $1
RETURNING id, name, scopes, created_at, last_used_at, revoked;

-- name: RotateAPIKey :one
-- Replace a key's secret in place (same id/name/scopes), clearing revoked/expiry.
UPDATE api_keys SET key_hash = $2, revoked = false, expires_at = NULL, last_used_at = NULL
WHERE id = $1
RETURNING id, name, scopes, created_at, last_used_at, revoked;

-- name: CountActiveAPIKeys :one
SELECT count(*) FROM api_keys WHERE revoked = false;

-- name: ListAPIKeys :many
SELECT id, name, scopes, project_ids, created_at, last_used_at, revoked FROM api_keys
ORDER BY created_at DESC;
