-- name: CreateProjectShare :one
INSERT INTO project_shares (
    project_id, peer_name, mailbox_url,
    send_mailbox, send_token, recv_mailbox, recv_token,
    secret, client_cert, client_key, ca_pem
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetProjectShare :one
SELECT * FROM project_shares WHERE id = $1;

-- name: ListProjectShares :many
SELECT * FROM project_shares ORDER BY created_at;

-- name: ListEnabledProjectShares :many
-- Drives the sync loop: every share it should be exchanging messages for.
SELECT * FROM project_shares WHERE enabled ORDER BY created_at;

-- name: ListSharesForProject :many
SELECT * FROM project_shares WHERE project_id = $1 ORDER BY created_at;

-- name: DeleteProjectShare :exec
DELETE FROM project_shares WHERE id = $1;

-- name: SetShareEnabled :exec
UPDATE project_shares SET enabled = $2, updated_at = now() WHERE id = $1;

-- name: RecordShareSend :exec
UPDATE project_shares SET last_send_at = now(), last_error = '', updated_at = now()
WHERE id = $1;

-- name: RecordShareRecv :exec
UPDATE project_shares SET last_recv_at = now(), last_error = '', updated_at = now()
WHERE id = $1;

-- name: RecordShareError :exec
-- Surfaced in the UI so a share that silently stopped working is visible
-- rather than just quietly stale.
UPDATE project_shares SET last_error = $2, updated_at = now() WHERE id = $1;

-- name: GetShareState :one
SELECT * FROM project_share_state
WHERE share_id = $1 AND entity_kind = $2 AND entity_id = $3;

-- name: ListShareStateHashes :many
SELECT entity_kind, entity_id, sent_hash, base_hash FROM project_share_state
WHERE share_id = $1;

-- name: RecordSent :exec
-- Advances only the echo guard. base_hash is deliberately untouched: we have
-- put this content on the wire, but the peer has not confirmed it, and it may
-- be editing the same row right now.
INSERT INTO project_share_state (share_id, entity_kind, entity_id, sent_hash)
VALUES ($1, $2, $3, $4)
ON CONFLICT (share_id, entity_kind, entity_id)
DO UPDATE SET sent_hash = EXCLUDED.sent_hash, synced_at = now();

-- name: RecordApplied :exec
-- After applying a peer's change both sides hold this content, so it becomes
-- the new agreed base AND the echo guard.
INSERT INTO project_share_state (share_id, entity_kind, entity_id, sent_hash, base_hash)
VALUES ($1, $2, $3, $4, $4)
ON CONFLICT (share_id, entity_kind, entity_id)
DO UPDATE SET sent_hash = EXCLUDED.sent_hash, base_hash = EXCLUDED.base_hash, synced_at = now();

-- name: RecordMergedBase :exec
-- A conflict merge produces content the peer has NOT seen. base_hash advances
-- to the merged result, but sent_hash is left behind so the outbound pass
-- notices the difference and pushes the merge back to the peer.
INSERT INTO project_share_state (share_id, entity_kind, entity_id, base_hash)
VALUES ($1, $2, $3, $4)
ON CONFLICT (share_id, entity_kind, entity_id)
DO UPDATE SET base_hash = EXCLUDED.base_hash, synced_at = now();

-- name: ListItemsForSync :many
-- Everything in the project, soft-deleted rows included: a deletion is a change
-- the peer needs to hear about.
SELECT * FROM items WHERE project_id = $1;

-- name: ListActivityForSync :many
SELECT * FROM activity WHERE project_id = $1 ORDER BY created_at;

-- name: UpsertSyncedItem :one
-- Applies a peer's item. Matched on the shared UUID, never on ref/seq, which
-- are per-instance. seq is supplied so the items_assign_ref trigger (which
-- fires only WHEN NEW.seq IS NULL) assigns this instance's own local ref.
INSERT INTO items (
    id, project_id, type, title, body, status, priority, assignee,
    source, external_ref, tags, metadata, acceptance_criteria,
    created_at, updated_at, closed_at, deleted_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8,
    $9, $10, $11, $12, $13,
    $14, $15, $16, $17
)
ON CONFLICT (id) DO UPDATE SET
    type                = EXCLUDED.type,
    title               = EXCLUDED.title,
    body                = EXCLUDED.body,
    status              = EXCLUDED.status,
    priority            = EXCLUDED.priority,
    assignee            = EXCLUDED.assignee,
    external_ref        = EXCLUDED.external_ref,
    tags                = EXCLUDED.tags,
    metadata            = EXCLUDED.metadata,
    acceptance_criteria = EXCLUDED.acceptance_criteria,
    updated_at          = EXCLUDED.updated_at,
    closed_at           = EXCLUDED.closed_at,
    deleted_at          = EXCLUDED.deleted_at,
    -- Content changed under it, so the local embedding is stale; NULL makes the
    -- background embedder pick it up again.
    embedding           = NULL,
    embedding_model     = '',
    version             = items.version + 1
RETURNING *;

-- name: UpsertSyncedActivity :exec
-- Activity is append-only, so a conflict means we have already seen this row
-- and there is nothing to change.
INSERT INTO activity (id, project_id, item_id, kind, actor, body, confidence, metadata, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO NOTHING;

-- name: GetItemForSync :one
SELECT * FROM items WHERE id = $1;
