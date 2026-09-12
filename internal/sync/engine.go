package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"flightdeck/internal/store"
)

// Interval is how often each share exchanges mail. Short enough that a shared
// project feels live, long enough that an idle share is a trickle of requests.
const Interval = 10 * time.Second

// conflictSeparator marks where two simultaneously-edited bodies were joined.
// It doubles as the idempotency marker: seeing it means a merge already
// happened here.
const conflictSeparator = "\n\n---\n_Edited on both instances; the other version:_\n\n"

// Engine runs the sync loop for every enabled share.
type Engine struct {
	St         *store.Store
	InstanceID string

	// OnChange is called after a share applies remote changes, so the rest of
	// the process can drop caches and push the UI update.
	OnChange func(projectID uuid.UUID)
}

// Run syncs every enabled share on a ticker until ctx is cancelled. One pass
// runs immediately so a freshly accepted invite populates without waiting.
func (e *Engine) Run(ctx context.Context) {
	e.runOnce(ctx)
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.runOnce(ctx)
		}
	}
}

func (e *Engine) runOnce(ctx context.Context) {
	shares, err := e.St.ListEnabledProjectShares(ctx)
	if err != nil {
		log.Printf("sync: list shares: %v", err)
		return
	}
	for _, share := range shares {
		if err := e.SyncShare(ctx, share); err != nil {
			// One broken share must not stop the others. The error is recorded
			// on the share so a silently dead link is visible rather than just
			// quietly stale.
			log.Printf("sync: share %s: %v", share.ID, err)
			_ = e.St.RecordShareError(ctx, store.RecordShareErrorParams{ID: share.ID, LastError: err.Error()})
			if errors.Is(err, ErrMailboxGone) {
				log.Printf("sync: disabling share %s — the peer revoked it", share.ID)
				_ = e.St.SetShareEnabled(ctx, store.SetShareEnabledParams{ID: share.ID, Enabled: false})
			}
		}
	}
}

// SyncShare runs one full exchange: apply anything waiting for us, then send
// anything of ours the peer has not seen.
//
// Inbound first on purpose. Applying the peer's changes before scanning for
// local ones means a row we just received is already recorded as agreed, so the
// outbound pass correctly sees it as unchanged instead of echoing it back.
func (e *Engine) SyncShare(ctx context.Context, share store.ProjectShare) error {
	client, err := NewClient(share.MailboxUrl, share.ClientCert, share.ClientKey, share.CaPem)
	if err != nil {
		return fmt.Errorf("mailbox client: %w", err)
	}
	if err := e.pullInbound(ctx, client, share); err != nil {
		return err
	}
	return e.pushOutbound(ctx, client, share)
}

// pullInbound drains our mailbox, applies each message, and only then acks.
// A crash before the ack means the message is redelivered and applied again —
// which is safe, because every apply is idempotent.
func (e *Engine) pullInbound(ctx context.Context, client *Client, share store.ProjectShare) error {
	msgs, err := client.Receive(ctx, share.RecvMailbox, share.RecvToken)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil
	}
	var applied []int64
	changed := false
	for _, msg := range msgs {
		env, err := Open(share.Secret, msg.Body)
		if err != nil {
			// Undecryptable mail is never going to become decryptable. Ack it
			// so one poisoned message can't wedge the share forever.
			log.Printf("sync: share %s: discarding message %d: %v", share.ID, msg.Seq, err)
			applied = append(applied, msg.Seq)
			continue
		}
		if env.Origin == e.InstanceID {
			// Our own message came back to us. Nothing to do but clear it.
			applied = append(applied, msg.Seq)
			continue
		}
		if err := e.apply(ctx, share, env); err != nil {
			// A real failure: leave it unacked so it is retried next pass
			// rather than silently lost.
			return fmt.Errorf("apply message %d: %w", msg.Seq, err)
		}
		applied = append(applied, msg.Seq)
		changed = true
	}
	if err := client.Ack(ctx, share.RecvMailbox, share.RecvToken, applied); err != nil {
		return err
	}
	_ = e.St.RecordShareRecv(ctx, share.ID)
	if changed && e.OnChange != nil {
		e.OnChange(share.ProjectID)
	}
	return nil
}

// apply writes one remote change locally and records that we now agree with the
// peer about that row.
func (e *Engine) apply(ctx context.Context, share store.ProjectShare, env Envelope) error {
	switch env.Kind {
	case KindItem:
		return e.applyItem(ctx, share, env)
	case KindActivity:
		return e.applyActivity(ctx, share, env)
	case KindProject:
		return e.applyProject(ctx, share, env)
	default:
		// Forward compatibility: a newer peer may send kinds we don't know.
		// Ignoring them is better than refusing the whole share.
		log.Printf("sync: share %s: ignoring unknown message kind %q", share.ID, env.Kind)
		return nil
	}
}

func (e *Engine) applyItem(ctx context.Context, share store.ProjectShare, env Envelope) error {
	var remote ItemPayload
	if err := json.Unmarshal(env.Payload, &remote); err != nil {
		return fmt.Errorf("decode item: %w", err)
	}
	id, err := uuid.Parse(remote.ID)
	if err != nil {
		return fmt.Errorf("item id: %w", err)
	}

	merged := false
	local, err := e.St.GetItemForSync(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// New to us.
	case err != nil:
		return err
	default:
		// The row exists on both sides. Did we change it since the last state
		// we and the peer provably agreed on? If not, theirs simply supersedes
		// ours. Note this compares against base_hash, not what we last sent:
		// sending is optimistic, and the peer may have been editing the same
		// row at the same moment.
		st, hashErr := e.shareState(ctx, share.ID, KindItem, id)
		if hashErr != nil {
			return hashErr
		}
		localHash, hashErr := ContentHash(itemPayload(local))
		if hashErr != nil {
			return hashErr
		}
		if localHash != st.base {
			// Both sides edited. Nothing is allowed to be overwritten, so the
			// two versions are merged and the displaced values are preserved
			// in the activity log rather than discarded.
			merged = true
			remote = mergeConflict(e.InstanceID, env.Origin, local, remote)
			if err := e.logConflict(ctx, share, local, remote); err != nil {
				return err
			}
		}
	}

	saved, err := e.St.UpsertSyncedItem(ctx, upsertParams(share.ProjectID, remote))
	if err != nil {
		return fmt.Errorf("upsert item: %w", err)
	}
	hash, err := ContentHash(itemPayload(saved))
	if err != nil {
		return err
	}
	if merged {
		// The merge is content the peer has never seen. Advance only the agreed
		// base; leaving sent_hash behind makes the next outbound pass push the
		// merged result back, so both sides end up with the combined version.
		return e.St.RecordMergedBase(ctx, store.RecordMergedBaseParams{
			ShareID: share.ID, EntityKind: KindItem, EntityID: id, BaseHash: hash,
		})
	}
	// Straight apply: both sides now hold exactly this, so it is both the new
	// base and the echo guard.
	return e.St.RecordApplied(ctx, store.RecordAppliedParams{
		ShareID: share.ID, EntityKind: KindItem, EntityID: id, SentHash: hash,
	})
}

// mergeConflict resolves a two-sided edit without destroying either version.
//
// Bodies are prose, so both survive, joined. Scalars cannot be appended, so one
// value has to win — but the displaced one is written to the activity log by
// logConflict, so nothing is actually lost.
//
// The critical property is DETERMINISM: both instances run this independently
// and must reach byte-identical results, or they settle into permanently
// different states and never speak again (each believing it already sent what
// it has). So the tiebreak is the instance ids, which both sides know: the
// lower id's text always goes first, and the higher id's scalars always win.
// Nothing here may depend on which side is doing the merging.
func mergeConflict(localID, remoteID string, local store.Item, remote ItemPayload) ItemPayload {
	// Scalars: a fixed winner, computed the same way on both instances.
	if localID > remoteID {
		remote.Title = local.Title
		remote.Status = local.Status
		remote.Priority = local.Priority
		remote.Type = local.Type
		remote.Assignee = local.Assignee
		remote.Tags = local.Tags
	}
	// Containment checks keep this idempotent. The merged text travels back to
	// the peer, which is itself mid-conflict and would otherwise append the same
	// passage again on every exchange, growing the body without ever settling.
	switch {
	case local.Body == remote.Body || local.Body == "":
	case remote.Body == "":
		remote.Body = local.Body
	case strings.Contains(remote.Body, local.Body):
		// The peer already merged ours in; take theirs as the settled version.
	case strings.Contains(local.Body, remote.Body):
		remote.Body = local.Body
	case localID < remoteID:
		remote.Body = local.Body + conflictSeparator + remote.Body
	default:
		remote.Body = remote.Body + conflictSeparator + local.Body
	}
	// Keep the earliest creation time: the item is as old as its oldest copy.
	if local.CreatedAt.Before(remote.CreatedAt) {
		remote.CreatedAt = local.CreatedAt
	}
	// A deletion on either side wins — resurrecting something someone deleted
	// is more surprising than honouring it.
	if local.DeletedAt != nil && remote.DeletedAt == nil {
		remote.DeletedAt = local.DeletedAt
	}
	return remote
}

// logConflict records what a conflicting edit displaced, so the "why" survives
// even when a field could not hold both values.
func (e *Engine) logConflict(ctx context.Context, share store.ProjectShare, local store.Item, remote ItemPayload) error {
	var changes []string
	if local.Status != remote.Status {
		changes = append(changes, fmt.Sprintf("status %s → %s", local.Status, remote.Status))
	}
	if local.Priority != remote.Priority {
		changes = append(changes, fmt.Sprintf("priority %s → %s", local.Priority, remote.Priority))
	}
	if local.Title != remote.Title {
		changes = append(changes, fmt.Sprintf("title %q → %q", local.Title, remote.Title))
	}
	if len(changes) == 0 {
		return nil
	}
	peer := share.PeerName
	if peer == "" {
		peer = "the other instance"
	}
	body := fmt.Sprintf("Sync conflict: this item was edited here and on %s at the same time. Bodies were merged; %s. The previous local values are preserved in this note.",
		peer, joinChanges(changes))
	_, err := e.St.CreateActivity(ctx, store.CreateActivityParams{
		ProjectID: local.ProjectID,
		ItemID:    pgtype.UUID{Bytes: local.ID, Valid: true},
		Kind:      strptr("comment"),
		Actor:     strptr("sync"),
		Body:      &body,
	})
	return err
}

func joinChanges(c []string) string {
	switch len(c) {
	case 1:
		return c[0]
	case 2:
		return c[0] + " and " + c[1]
	default:
		out := ""
		for i, s := range c[:len(c)-1] {
			if i > 0 {
				out += ", "
			}
			out += s
		}
		return out + ", and " + c[len(c)-1]
	}
}

func (e *Engine) applyActivity(ctx context.Context, share store.ProjectShare, env Envelope) error {
	var remote ActivityPayload
	if err := json.Unmarshal(env.Payload, &remote); err != nil {
		return fmt.Errorf("decode activity: %w", err)
	}
	id, err := uuid.Parse(remote.ID)
	if err != nil {
		return fmt.Errorf("activity id: %w", err)
	}
	var itemID pgtype.UUID
	if remote.ItemID != nil {
		parsed, err := uuid.Parse(*remote.ItemID)
		if err == nil {
			// Only attach to the item if we actually have it; ordering across
			// the two mailboxes is not guaranteed, and an activity must never
			// fail on a foreign key for an item still in flight.
			if _, err := e.St.GetItemForSync(ctx, parsed); err == nil {
				itemID = pgtype.UUID{Bytes: parsed, Valid: true}
			}
		}
	}
	confidence := ""
	if remote.Confidence != nil {
		confidence = *remote.Confidence
	}
	err = e.St.UpsertSyncedActivity(ctx, store.UpsertSyncedActivityParams{
		ID:         id,
		ProjectID:  share.ProjectID,
		ItemID:     itemID,
		Kind:       remote.Kind,
		Actor:      remote.Actor,
		Body:       remote.Body,
		Confidence: confidence,
		Metadata:   orEmptyJSON(remote.Metadata),
		CreatedAt:  remote.CreatedAt,
	})
	if err != nil {
		return fmt.Errorf("upsert activity: %w", err)
	}
	return e.recordApplied(ctx, share.ID, KindActivity, id, remote)
}

func (e *Engine) applyProject(ctx context.Context, share store.ProjectShare, env Envelope) error {
	var remote ProjectPayload
	if err := json.Unmarshal(env.Payload, &remote); err != nil {
		return fmt.Errorf("decode project: %w", err)
	}
	// Slug is intentionally not synced — it is globally unique per instance and
	// each side names the project locally.
	saved, err := e.St.UpdateProject(ctx, store.UpdateProjectParams{
		Slug:         projectSlug(ctx, e.St, share.ProjectID),
		Name:         &remote.Name,
		Summary:      &remote.Summary,
		Instructions: &remote.Instructions,
	})
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	// Record what we actually stored, not what arrived — the same rule
	// applyItem follows, so the outbound pass sees this row as settled.
	return e.recordApplied(ctx, share.ID, KindProject, share.ProjectID, ProjectPayload{
		ID: saved.ID.String(), Name: saved.Name, Summary: saved.Summary,
		Instructions: saved.Instructions,
	})
}

// pushOutbound sends every row whose content differs from what we last put on
// the wire. The hash comparison is the echo guard: a row we applied from the peer
// hashes to exactly what we recorded, so it is not sent back.
func (e *Engine) pushOutbound(ctx context.Context, client *Client, share store.ProjectShare) error {
	sentHashes, err := e.sentHashes(ctx, share.ID)
	if err != nil {
		return err
	}
	sent := 0

	send := func(kind string, id uuid.UUID, payload any) error {
		hash, err := ContentHash(payload)
		if err != nil {
			return err
		}
		if sentHashes[stateKey{kind, id}] == hash {
			return nil // we already sent exactly this
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		sealed, err := Seal(share.Secret, Envelope{
			Version: ProtocolVersion,
			Kind:    kind,
			Origin:  e.InstanceID,
			SentAt:  time.Now().UTC(),
			Payload: raw,
		})
		if err != nil {
			return err
		}
		if err := client.Send(ctx, share.SendMailbox, share.SendToken, sealed); err != nil {
			return err
		}
		sent++
		return e.St.RecordSent(ctx, store.RecordSentParams{
			ShareID: share.ID, EntityKind: kind, EntityID: id, SentHash: hash,
		})
	}

	project, err := e.St.GetProjectByID(ctx, share.ProjectID)
	if err != nil {
		return err
	}
	if err := send(KindProject, project.ID, ProjectPayload{
		ID: project.ID.String(), Name: project.Name, Summary: project.Summary,
		Instructions: project.Instructions,
	}); err != nil {
		return err
	}

	items, err := e.St.ListItemsForSync(ctx, share.ProjectID)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := send(KindItem, it.ID, itemPayload(it)); err != nil {
			return err
		}
	}

	activity, err := e.St.ListActivityForSync(ctx, share.ProjectID)
	if err != nil {
		return err
	}
	for _, a := range activity {
		if err := send(KindActivity, a.ID, activityPayload(a)); err != nil {
			return err
		}
	}

	if sent > 0 {
		log.Printf("sync: share %s: sent %d change(s)", share.ID, sent)
		_ = e.St.RecordShareSend(ctx, share.ID)
	}
	return nil
}

type stateKey struct {
	kind string
	id   uuid.UUID
}

func (e *Engine) sentHashes(ctx context.Context, shareID uuid.UUID) (map[stateKey]string, error) {
	rows, err := e.St.ListShareStateHashes(ctx, shareID)
	if err != nil {
		return nil, err
	}
	out := make(map[stateKey]string, len(rows))
	for _, r := range rows {
		out[stateKey{r.EntityKind, r.EntityID}] = r.SentHash
	}
	return out, nil
}

type syncState struct{ sent, base string }

func (e *Engine) shareState(ctx context.Context, shareID uuid.UUID, kind string, id uuid.UUID) (syncState, error) {
	row, err := e.St.GetShareState(ctx, store.GetShareStateParams{ShareID: shareID, EntityKind: kind, EntityID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return syncState{}, nil
	}
	if err != nil {
		return syncState{}, err
	}
	return syncState{sent: row.SentHash, base: row.BaseHash}, nil
}

func (e *Engine) recordApplied(ctx context.Context, shareID uuid.UUID, kind string, id uuid.UUID, payload any) error {
	hash, err := ContentHash(payload)
	if err != nil {
		return err
	}
	return e.St.RecordApplied(ctx, store.RecordAppliedParams{
		ShareID: shareID, EntityKind: kind, EntityID: id, SentHash: hash,
	})
}

func itemPayload(it store.Item) ItemPayload {
	return ItemPayload{
		ID: it.ID.String(), Type: it.Type, Title: it.Title, Body: it.Body,
		Status: it.Status, Priority: it.Priority, Assignee: it.Assignee,
		ExternalRef: it.ExternalRef, Tags: it.Tags,
		Metadata: orEmptyJSON(it.Metadata), AcceptanceCriteria: orEmptyJSON(it.AcceptanceCriteria),
		CreatedAt: it.CreatedAt.UTC(),
		ClosedAt:  utcPtr(it.ClosedAt), DeletedAt: utcPtr(it.DeletedAt),
	}
}

func activityPayload(a store.Activity) ActivityPayload {
	var itemID *string
	if a.ItemID.Valid {
		s := uuid.UUID(a.ItemID.Bytes).String()
		itemID = &s
	}
	var conf *string
	if a.Confidence != "" {
		c := a.Confidence
		conf = &c
	}
	return ActivityPayload{
		ID: a.ID.String(), ItemID: itemID, Kind: a.Kind, Actor: a.Actor,
		Body: a.Body, Confidence: conf, Metadata: orEmptyJSON(a.Metadata),
		CreatedAt: a.CreatedAt.UTC(),
	}
}

func upsertParams(projectID uuid.UUID, p ItemPayload) store.UpsertSyncedItemParams {
	id, _ := uuid.Parse(p.ID)
	return store.UpsertSyncedItemParams{
		ID: id, ProjectID: projectID, Type: p.Type, Title: p.Title, Body: p.Body,
		Status: p.Status, Priority: p.Priority, Assignee: p.Assignee,
		// Marks where the row came from; the CHECK allows 'api'.
		// tags is NOT NULL, and an empty slice round-trips through JSON as nil
		// (omitempty), so it has to be restored rather than passed through.
		Source: "api", ExternalRef: p.ExternalRef, Tags: orEmptySlice(p.Tags),
		Metadata: orEmptyJSON(p.Metadata), AcceptanceCriteria: orEmptyJSONArray(p.AcceptanceCriteria),
		// updated_at is not synced; the BEFORE UPDATE trigger owns it. Passing
		// created_at keeps it as the local value on insert.
		CreatedAt: p.CreatedAt, UpdatedAt: p.CreatedAt,
		ClosedAt: p.ClosedAt, DeletedAt: p.DeletedAt,
	}
}

func orEmptyJSON(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return b
}

func orEmptyJSONArray(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("[]")
	}
	return b
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func strptr(s string) *string { return &s }

// projectSlug resolves a project's slug for the update path. A share always
// points at a project that exists, so a lookup failure here means the row was
// deleted mid-sync and the empty slug simply matches nothing.
func projectSlug(ctx context.Context, st *store.Store, id uuid.UUID) string {
	p, err := st.GetProjectByID(ctx, id)
	if err != nil {
		return ""
	}
	return p.Slug
}

// orEmptySlice restores a non-nil slice for NOT NULL columns. An empty []string
// marshals away under omitempty and comes back as nil, which Postgres rejects.
func orEmptySlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
