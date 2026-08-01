// Package integration holds DB-backed tests for the store + service layers. They
// run only when FLIGHTDECK_TEST_DB points at a disposable Postgres (CI provides a
// service container); otherwise they skip, so `go test ./...` stays unit-only and
// hermetic. Each test truncates the schema first for isolation.
package integration

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"flightdeck/internal/embed"
	"flightdeck/internal/pgvec"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

func setup(t testing.TB) (*store.Store, *service.Service) {
	t.Helper()
	url := os.Getenv("FLIGHTDECK_TEST_DB")
	if url == "" {
		t.Skip("set FLIGHTDECK_TEST_DB to run integration tests")
	}
	if err := store.Migrate(url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st, err := store.NewStore(context.Background(), url)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(st.Close)
	_, err = st.Pool.Exec(context.Background(),
		`TRUNCATE webhook_events, activity, item_links, item_refs, items, webhooks, projects RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return st, service.New(st)
}

func mkProject(t testing.TB, st *store.Store, slug string) store.Project {
	t.Helper()
	p, err := st.CreateProject(context.Background(), store.CreateProjectParams{Slug: slug, Name: slug})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return p
}

func countRows(t testing.TB, st *store.Store, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := st.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// Refs are assigned per-project, gapless, and unique.
func TestRefAssignment(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	a := mkProject(t, st, "alpha")
	b := mkProject(t, st, "beta")

	i1, _ := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: a.ID, Title: "one"}, "tester")
	i2, _ := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: a.ID, Title: "two"}, "tester")
	i3, _ := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: b.ID, Title: "other"}, "tester")

	if i1.Ref != "alpha-1" || i2.Ref != "alpha-2" || i3.Ref != "beta-1" {
		t.Fatalf("unexpected refs: %s %s %s", i1.Ref, i2.Ref, i3.Ref)
	}
	got, err := st.GetItemByRef(ctx, "ALPHA-2") // case-insensitive
	if err != nil || got.ID != i2.ID {
		t.Fatalf("GetItemByRef: %v (%v)", err, got.ID)
	}
}

// CreateItem appends a 'created' activity and enqueues an outbox event, in one tx.
func TestCreateItemSideEffects(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")
	item, err := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "task"}, "tester")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if n := countRows(t, st, `SELECT count(*) FROM activity WHERE item_id=$1 AND kind='created'`, item.ID); n != 1 {
		t.Fatalf("created activity = %d, want 1", n)
	}
	if n := countRows(t, st, `SELECT count(*) FROM webhook_events WHERE event='item.created'`); n != 1 {
		t.Fatalf("outbox rows = %d, want 1", n)
	}
}

// A status change logs a status_change activity and enqueues an event.
func TestStatusChange(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")
	item, _ := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "task"}, "tester")
	done := "done"
	if _, err := svc.UpdateItem(ctx, store.UpdateItemParams{ID: item.ID, Status: &done}, "tester"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if n := countRows(t, st, `SELECT count(*) FROM activity WHERE item_id=$1 AND kind='status_change'`, item.ID); n != 1 {
		t.Fatalf("status_change activity = %d, want 1", n)
	}
	if n := countRows(t, st, `SELECT count(*) FROM webhook_events WHERE event='item.status_changed'`); n != 1 {
		t.Fatalf("status_changed events = %d, want 1", n)
	}
}

// Optimistic concurrency: a stale expected_version is rejected as a conflict.
func TestOptimisticConcurrency(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")
	item, _ := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "task"}, "tester")
	if item.Version != 1 {
		t.Fatalf("initial version = %d, want 1", item.Version)
	}
	v1 := int32(1)
	body := "edit"
	up, err := svc.UpdateItem(ctx, store.UpdateItemParams{ID: item.ID, Body: &body, ExpectedVersion: &v1}, "tester")
	if err != nil {
		t.Fatalf("first update: %v", err)
	}
	if up.Version != 2 {
		t.Fatalf("version after update = %d, want 2", up.Version)
	}
	// Re-using the stale version 1 must conflict.
	if _, err := svc.UpdateItem(ctx, store.UpdateItemParams{ID: item.ID, Body: &body, ExpectedVersion: &v1}, "tester"); !errors.Is(err, service.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

// ListItems pagination via limit/offset returns disjoint, ordered pages.
func TestPagination(t *testing.T) {
	st, _ := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")
	svc := service.New(st)
	for i := 0; i < 5; i++ {
		if _, err := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "t"}, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	lim, off := int32(2), int32(2)
	page, err := st.ListItems(ctx, store.ListItemsParams{ProjectID: pgtype.UUID{Bytes: p.ID, Valid: true}, Lim: &lim, Off: &off})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("page size = %d, want 2", len(page))
	}
}

// Trigram fuzzy search matches a typo'd title that FTS would miss.
func TestFuzzySearch(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")
	title := "authentication flow"
	if _, err := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: title}, "tester"); err != nil {
		t.Fatal(err)
	}
	rows, err := st.SearchItemsFuzzy(ctx, store.SearchItemsFuzzyParams{Q: "authentcation"}) // missing 'i'
	if err != nil {
		t.Fatalf("fuzzy: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("fuzzy search found nothing for a typo")
	}
}

// Project-filtered counts only count the requested project's items.
func TestProjectFilteredCounts(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	a := mkProject(t, st, "alpha")
	b := mkProject(t, st, "beta")
	svc.CreateItem(ctx, store.CreateItemParams{ProjectID: a.ID, Title: "a1"}, "tester")
	svc.CreateItem(ctx, store.CreateItemParams{ProjectID: a.ID, Title: "a2"}, "tester")
	svc.CreateItem(ctx, store.CreateItemParams{ProjectID: b.ID, Title: "b1"}, "tester")
	counts, err := st.CountItemsByStatusForProject(ctx, a.ID)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	total := 0
	for _, c := range counts {
		total += int(c.N)
	}
	if total != 2 {
		t.Fatalf("alpha item count = %d, want 2", total)
	}
}

// LeaseWebhookEvents claims a due event and hides it from a second lease.
func TestWebhookLeaseHidesEvent(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")
	svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "task"}, "tester") // enqueues item.created

	leased, err := st.LeaseWebhookEvents(ctx, 10)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if len(leased) != 1 {
		t.Fatalf("leased = %d, want 1", len(leased))
	}
	again, _ := st.LeaseWebhookEvents(ctx, 10) // the lease pushed next_attempt out
	if len(again) != 0 {
		t.Fatalf("second lease = %d, want 0 (lease should hide it)", len(again))
	}
}

// The worker delivers a due event and, with no subscribers, marks it delivered.
func TestWebhookWorkerDelivers(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")
	svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "task"}, "tester")

	svc.RunWebhookWorkerOnce(ctx)
	if n := countRows(t, st, `SELECT count(*) FROM webhook_events WHERE delivered_at IS NOT NULL`); n != 1 {
		t.Fatalf("delivered events = %d, want 1", n)
	}
}

// unitVec builds a 1536-dim embedding pointing mostly along axis `axis`, so
// crafted vectors have predictable cosine distances without calling OpenAI.
func unitVec(axis int) pgvec.Vector {
	v := make([]float32, embed.Dims)
	v[axis%embed.Dims] = 1
	return pgvec.New(v)
}

// Semantic search returns nearest neighbours by cosine distance, respects the
// max_distance threshold, and honours the project filter.
func TestSemanticSearch(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")

	near, _ := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "near"}, "t")
	far, _ := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "far"}, "t")

	// near aligns with the query axis (distance 0); far is orthogonal (distance 1).
	mustSetEmbedding(t, st, near.ID, unitVec(0))
	mustSetEmbedding(t, st, far.ID, unitVec(1))

	pid := pgtype.UUID{Bytes: p.ID, Valid: true}
	rows, err := st.SearchItemsSemantic(ctx, store.SearchItemsSemanticParams{
		QueryEmbedding: unitVec(0),
		ProjectID:      pid,
		MaxDistance:    0.5, // excludes the orthogonal "far" item (distance 1)
	})
	if err != nil {
		t.Fatalf("semantic search: %v", err)
	}
	if len(rows) != 1 || rows[0].Ref != near.Ref {
		t.Fatalf("got %d rows, want only %s", len(rows), near.Ref)
	}

	// Widening the threshold surfaces both, nearest first.
	rows, _ = st.SearchItemsSemantic(ctx, store.SearchItemsSemanticParams{
		QueryEmbedding: unitVec(0), ProjectID: pid, MaxDistance: 2,
	})
	if len(rows) != 2 || rows[0].Ref != near.Ref {
		t.Fatalf("widened: got %d rows (first=%s), want 2 with near first", len(rows), rows[0].Ref)
	}
}

// A content edit invalidates the embedding so the embedder re-picks it up.
func TestEmbeddingInvalidatedOnEdit(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "alpha")
	it, _ := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: "orig"}, "t")
	mustSetEmbedding(t, st, it.ID, unitVec(0))

	if n := backlogCount(t, st); n != 0 {
		t.Fatalf("backlog before edit = %d, want 0", n)
	}
	newTitle := "edited"
	if _, err := svc.UpdateItem(ctx, store.UpdateItemParams{ID: it.ID, Title: &newTitle}, "t"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if n := backlogCount(t, st); n != 1 {
		t.Fatalf("backlog after title edit = %d, want 1 (embedding should be NULLed)", n)
	}
}

func mustSetEmbedding(t *testing.T, st *store.Store, id uuid.UUID, v pgvec.Vector) {
	t.Helper()
	if err := st.SetItemEmbedding(context.Background(), store.SetItemEmbeddingParams{
		ID: id, Embedding: v, EmbeddingModel: "test",
	}); err != nil {
		t.Fatalf("set embedding: %v", err)
	}
}

func backlogCount(t *testing.T, st *store.Store) int {
	t.Helper()
	rows, err := st.ListItemsNeedingEmbedding(context.Background(), 100)
	if err != nil {
		t.Fatalf("list backlog: %v", err)
	}
	return len(rows)
}
