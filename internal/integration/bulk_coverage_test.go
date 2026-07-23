package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"flightdeck/internal/store"
)

// TestBulkCreateItemsAtomic verifies the single-transaction rewrite: a happy
// batch creates every item, and a batch that fails partway creates NONE (the
// whole tx rolls back) rather than leaving a partial commit.
func TestBulkCreateItemsAtomic(t *testing.T) {
	st, svc := setup(t)
	p := mkProject(t, st, "bulk")
	ctx := context.Background()

	items, err := svc.BulkCreateItems(ctx, []store.CreateItemParams{
		{ProjectID: p.ID, Title: "one"},
		{ProjectID: p.ID, Title: "two"},
	}, "tester")
	if err != nil {
		t.Fatalf("happy batch: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("returned %d items, want 2", len(items))
	}
	if n := countRows(t, st, "SELECT count(*) FROM items WHERE project_id=$1", p.ID); n != 2 {
		t.Fatalf("items in db = %d, want 2", n)
	}
	// Each item must also have appended its 'created' activity within the tx.
	if n := countRows(t, st, "SELECT count(*) FROM activity WHERE project_id=$1 AND kind='created'", p.ID); n != 2 {
		t.Fatalf("created activities = %d, want 2", n)
	}

	// Second item references a non-existent project → FK violation mid-batch.
	_, err = svc.BulkCreateItems(ctx, []store.CreateItemParams{
		{ProjectID: p.ID, Title: "three"},
		{ProjectID: uuid.New(), Title: "orphan"},
	}, "tester")
	if err == nil {
		t.Fatal("expected an error from the bad batch, got nil")
	}
	if n := countRows(t, st, "SELECT count(*) FROM items WHERE project_id=$1", p.ID); n != 2 {
		t.Fatalf("after failed batch items = %d, want 2 (batch must roll back)", n)
	}
}

// TestEmbeddingCoverageReport verifies usage_report's new coverage counts: with
// no embedder run, items are all-total/zero-embedded (the "starved semantic
// tier" signal we added it to diagnose).
func TestEmbeddingCoverageReport(t *testing.T) {
	st, svc := setup(t)
	p := mkProject(t, st, "cov")
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := svc.CreateItem(ctx, store.CreateItemParams{
			ProjectID: p.ID, Title: fmt.Sprintf("item %d", i),
		}, "tester"); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	rep, err := svc.UsageReport(ctx, 7, nil)
	if err != nil {
		t.Fatalf("usage report: %v", err)
	}
	if rep.Coverage.ItemsTotal != 3 {
		t.Fatalf("items_total = %d, want 3", rep.Coverage.ItemsTotal)
	}
	if rep.Coverage.ItemsEmbedded != 0 {
		t.Fatalf("items_embedded = %d, want 0 (no embedder ran)", rep.Coverage.ItemsEmbedded)
	}
}
