-- name: CreateOAuthClient :one
INSERT INTO oauth_clients (id, secret_hash, name, redirect_uris, auth_method)
VALUES ($1, sqlc.narg('secret_hash'), $2, $3, $4)
RETURNING *;

-- name: GetOAuthClient :one
SELECT * FROM oauth_clients WHERE id = $1;

-- name: CreateOAuthCode :exec
INSERT INTO oauth_codes (code_hash, client_id, api_key_id, redirect_uri, code_challenge, scope, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ConsumeOAuthCode :one
-- Single use: the UPDATE only matches an unused, unexpired code, so a replay
-- gets no row.
UPDATE oauth_codes SET used = true
WHERE code_hash = $1 AND used = false AND expires_at > now()
RETURNING *;

-- name: CreateOAuthToken :one
INSERT INTO oauth_tokens (client_id, api_key_id, access_hash, refresh_hash, scope, access_expires_at, refresh_expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetOAuthTokenByAccessHash :one
-- Joins the backing key so a revoked or expired key invalidates its tokens.
SELECT t.id, t.api_key_id, t.scope, t.last_used_at, k.name AS key_name, k.scopes AS key_scopes
FROM oauth_tokens t
JOIN api_keys k ON k.id = t.api_key_id
WHERE t.access_hash = $1 AND t.revoked = false AND t.access_expires_at > now()
  AND k.revoked = false AND (k.expires_at IS NULL OR k.expires_at > now());

-- name: ConsumeOAuthRefresh :one
-- Rotation: revoke the row holding this refresh token and hand back what is
-- needed to mint its successor. A replayed refresh token matches nothing.
UPDATE oauth_tokens SET revoked = true
WHERE refresh_hash = $1 AND revoked = false AND refresh_expires_at > now()
RETURNING *;

-- name: TouchOAuthToken :exec
UPDATE oauth_tokens SET last_used_at = now() WHERE id = $1;

-- name: PurgeExpiredOAuth :execrows
-- Maintenance: codes and tokens past every usable window.
WITH c AS (DELETE FROM oauth_codes WHERE expires_at < now())
DELETE FROM oauth_tokens WHERE refresh_expires_at < now() OR (revoked AND created_at < now() - interval '1 day');
