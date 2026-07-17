-- name: PurgeSoftDeletedItems :execrows
-- Hard-delete items soft-deleted before the cutoff, reclaiming index space and
-- dropping FTS tombstones.
DELETE FROM items WHERE deleted_at IS NOT NULL AND deleted_at < $1;

-- name: PurgeOldActivity :execrows
-- Trim low-signal activity (comments and auto 'created' rows) older than the
-- cutoff. Decisions, progress, status changes, and rejections are kept — they
-- are the durable "why" an agent reads.
DELETE FROM activity
WHERE kind IN ('comment', 'created') AND created_at < $1;
