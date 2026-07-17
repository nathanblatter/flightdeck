package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"flightdeck/internal/store"
)

// Idempotency keys are scoped per project: the same key in two projects creates
// two distinct items instead of returning the other project's item.
func TestIdempotencyScopedToProject(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	a := mkProject(t, st, "alpha")
	b := mkProject(t, st, "beta")

	key := "fix-login-bug"
	ia, err := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: a.ID, Title: "in alpha", IdempotencyKey: &key}, "t")
	if err != nil {
		t.Fatalf("create in alpha: %v", err)
	}
	ib, err := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: b.ID, Title: "in beta", IdempotencyKey: &key}, "t")
	if err != nil {
		t.Fatalf("create in beta: %v", err)
	}
	if ia.ID == ib.ID {
		t.Fatalf("same item returned across projects for key %q", key)
	}
	if ib.ProjectID != b.ID {
		t.Fatalf("beta item landed in project %s", ib.ProjectID)
	}
	// Same project + same key must still dedupe.
	again, err := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: a.ID, Title: "retry", IdempotencyKey: &key}, "t")
	if err != nil || again.ID != ia.ID {
		t.Fatalf("retry in alpha: got %v (%v), want original item", again.ID, err)
	}
}

// CompleteItem writes the done status, the decision, and the summary refresh in
// one transaction.
func TestCompleteItem(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")
	it, _ := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "task"}, "t")

	updated, err := svc.CompleteItem(ctx, it.ID, "shipped because reasons", "new summary", "t")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if updated.Status != "done" {
		t.Fatalf("status = %s, want done", updated.Status)
	}
	if n := countRows(t, st, `SELECT count(*) FROM activity WHERE item_id=$1 AND kind='decision'`, it.ID); n != 1 {
		t.Fatalf("decision activities = %d, want 1", n)
	}
	got, _ := st.GetProjectBySlug(ctx, "alpha")
	if got.Summary != "new summary" {
		t.Fatalf("summary = %q, want refreshed", got.Summary)
	}
}

// Per-hook delivery: retries only re-POST to subscribers that failed, never to
// ones that already ACKed.
func TestWebhookPerHookDelivery(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")

	var goodHits, badHits atomic.Int32
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		goodHits.Add(1)
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		badHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	for _, url := range []string{good.URL, bad.URL} {
		if _, err := st.CreateWebhook(ctx, store.CreateWebhookParams{Url: url}); err != nil {
			t.Fatalf("create webhook: %v", err)
		}
	}
	svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "task"}, "t")

	svc.RunWebhookWorkerOnce(ctx) // attempt 1: good ACKs, bad 500s
	// Make the rescheduled event due again, then retry.
	if _, err := st.Pool.Exec(ctx, `UPDATE webhook_events SET next_attempt_at = now()`); err != nil {
		t.Fatal(err)
	}
	svc.RunWebhookWorkerOnce(ctx) // attempt 2: must skip good

	if got := goodHits.Load(); got != 1 {
		t.Fatalf("good subscriber POSTed %d times, want 1 (no duplicate delivery)", got)
	}
	if got := badHits.Load(); got != 2 {
		t.Fatalf("bad subscriber POSTed %d times, want 2 (still retrying)", got)
	}
	if n := countRows(t, st, `SELECT count(*) FROM webhook_events WHERE delivered_at IS NOT NULL OR parked_at IS NOT NULL`); n != 0 {
		t.Fatalf("event should still be pending, got %d finished", n)
	}
}

// Poison rows are parked and skipped by the backlog; a content edit un-parks an item.
func TestEmbeddingPoisonParking(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")
	it, _ := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "poison"}, "t")

	if n := backlogCount(t, st); n != 1 {
		t.Fatalf("backlog = %d, want 1", n)
	}
	if err := st.MarkItemEmbeddingFailed(ctx, it.ID); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if n := backlogCount(t, st); n != 0 {
		t.Fatalf("backlog after parking = %d, want 0", n)
	}
	newTitle := "edited content"
	if _, err := svc.UpdateItem(ctx, store.UpdateItemParams{ID: it.ID, Title: &newTitle}, "t"); err != nil {
		t.Fatal(err)
	}
	if n := backlogCount(t, st); n != 1 {
		t.Fatalf("backlog after edit = %d, want 1 (edit should clear the failed marker)", n)
	}
}

// Decision/progress activity flows through the embedding backlog and becomes
// semantically searchable; parked activity drops out of the backlog.
func TestActivityEmbedding(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")

	kind, actor := "decision", "t"
	body := "chose postgres over sqlite for concurrency"
	row, err := svc.LogActivity(ctx, store.CreateActivityParams{
		ProjectID: p.ID, Kind: &kind, Actor: &actor, Body: &body,
	})
	if err != nil {
		t.Fatalf("log activity: %v", err)
	}
	// Comments are low-signal and must NOT enter the backlog.
	ckind := "comment"
	cbody := "just a note"
	if _, err := svc.LogActivity(ctx, store.CreateActivityParams{ProjectID: p.ID, Kind: &ckind, Actor: &actor, Body: &cbody}); err != nil {
		t.Fatal(err)
	}

	backlog, err := st.ListActivityNeedingEmbedding(ctx, 100)
	if err != nil {
		t.Fatalf("activity backlog: %v", err)
	}
	if len(backlog) != 1 || backlog[0].ID != row.ID {
		t.Fatalf("backlog = %d rows, want just the decision", len(backlog))
	}

	if err := st.InsertActivityEmbedding(ctx, store.InsertActivityEmbeddingParams{
		ActivityID: row.ID, Embedding: unitVec(0), Model: "test",
	}); err != nil {
		t.Fatalf("insert embedding: %v", err)
	}
	if got, _ := st.ListActivityNeedingEmbedding(ctx, 100); len(got) != 0 {
		t.Fatalf("backlog after embed = %d, want 0", len(got))
	}

	hits, err := st.SearchActivitySemantic(ctx, store.SearchActivitySemanticParams{
		QueryEmbedding: unitVec(0), MaxDistance: 0.5,
	})
	if err != nil {
		t.Fatalf("semantic activity search: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != row.ID {
		t.Fatalf("semantic hits = %d, want the decision", len(hits))
	}
}
