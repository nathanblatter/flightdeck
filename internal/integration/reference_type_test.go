package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

// TestReferenceType covers flightdeck-93: a type=reference item is a durable
// fact, not work. It round-trips through create/update/list, is kept out of
// the work views (counts, open_items, ready_next, next_action, global
// top_items), rides along in ProjectContext.Reference, and is still found by
// search.
func TestReferenceType(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "refproj")

	typ := func(s string) *string { return &s }
	longBody := strings.Repeat("vin JT3HN86R0X0123456 plate ABC123 oil 0W-20 ", 10) // > 200 runes
	ref := mkItem(t, svc, store.CreateItemParams{
		ProjectID: p.ID, Type: typ("reference"), Title: "4Runner vehicle facts", Body: &longBody,
	}, "tester")
	if ref.Type != "reference" {
		t.Fatalf("created type = %q, want reference", ref.Type)
	}
	work := mkItem(t, svc, store.CreateItemParams{ProjectID: p.ID, Title: "rotate the tires"}, "tester")

	// Reclassify a note into a reference and back out through update.
	note := mkItem(t, svc, store.CreateItemParams{ProjectID: p.ID, Type: typ("note"), Title: "doordash account details"}, "tester")
	upd, err := svc.UpdateItem(ctx, store.UpdateItemParams{ID: note.ID, Type: typ("reference")}, "tester")
	if err != nil {
		t.Fatalf("update type: %v", err)
	}
	if upd.Type != "reference" {
		t.Fatalf("updated type = %q, want reference", upd.Type)
	}
	if _, err := svc.UpdateItem(ctx, store.UpdateItemParams{ID: note.ID, Type: typ("dossier")}, "tester"); err == nil {
		t.Fatal("unknown type accepted by UpdateItem")
	}

	// list_items with a type filter sees both references and nothing else.
	proj := pgtype.UUID{Bytes: p.ID, Valid: true}
	listed, err := st.ListItems(ctx, store.ListItemsParams{ProjectID: proj, Type: typ("reference")})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("list type=reference returned %d, want 2", len(listed))
	}

	// Project orient: facts in the reference block, out of the work views.
	bundle, err := svc.ProjectContext(ctx, "refproj", service.VerbosityCompact)
	if err != nil {
		t.Fatalf("project context: %v", err)
	}
	if got := bundle.Counts["backlog"]; got != 1 {
		t.Fatalf("counts.backlog = %d, want 1 (reference items excluded); counts=%v", got, bundle.Counts)
	}
	for _, it := range bundle.OpenItems {
		if it.Type == "reference" {
			t.Fatalf("open_items contains reference item %s", it.Ref)
		}
	}
	if len(bundle.OpenItems) != 1 || bundle.OpenItems[0].Ref != work.Ref {
		t.Fatalf("open_items = %+v, want only %s", bundle.OpenItems, work.Ref)
	}
	for _, b := range bundle.ReadyNext {
		if b.Type == "reference" {
			t.Fatalf("ready_next contains reference item %s", b.Ref)
		}
	}
	if len(bundle.Reference) != 2 {
		t.Fatalf("reference block has %d entries, want 2: %+v", len(bundle.Reference), bundle.Reference)
	}
	var found bool
	for _, r := range bundle.Reference {
		if r.Ref == ref.Ref {
			found = true
			if n := len([]rune(r.Body)); n > 201 || !strings.HasSuffix(r.Body, "…") {
				t.Fatalf("compact reference body not truncated: %d runes, %q", n, r.Body)
			}
		}
	}
	if !found {
		t.Fatalf("reference %s missing from reference block", ref.Ref)
	}
	full, err := svc.ProjectContext(ctx, "refproj", service.VerbosityFull)
	if err != nil {
		t.Fatalf("project context full: %v", err)
	}
	for _, r := range full.Reference {
		if r.Ref == ref.Ref && r.Body != longBody {
			t.Fatalf("full verbosity truncated the reference body: %q", r.Body)
		}
	}

	// next_action (ListReadyItems) never proposes a fact as the next thing to do.
	next, err := svc.NextAction(ctx, "refproj", 10)
	if err != nil {
		t.Fatalf("next action: %v", err)
	}
	for _, it := range next.Items {
		if it.Type == "reference" {
			t.Fatalf("next_action contains reference item %s", it.Ref)
		}
	}
	if len(next.Items) != 1 {
		t.Fatalf("next_action returned %d items, want 1", len(next.Items))
	}

	// Global orient: top_items and counts skip references too.
	global, err := svc.GlobalContext(ctx, service.VerbosityCompact)
	if err != nil {
		t.Fatalf("global context: %v", err)
	}
	for _, po := range global.Projects {
		if po.Project.Slug != "refproj" {
			continue
		}
		if got := po.Counts["backlog"]; got != 1 {
			t.Fatalf("global counts.backlog = %d, want 1; counts=%v", got, po.Counts)
		}
		for _, b := range po.TopItems {
			if b.Type == "reference" {
				t.Fatalf("global top_items contains reference item %s", b.Ref)
			}
		}
		if len(po.TopItems) != 1 {
			t.Fatalf("global top_items = %+v, want only %s", po.TopItems, work.Ref)
		}
	}

	// Search still finds facts — they are what an agent most often looks up.
	items, _, err := svc.SearchSmart(ctx, "vehicle facts", proj, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	found = false
	for _, it := range items {
		if it.Ref == ref.Ref {
			found = true
		}
	}
	if !found {
		t.Fatalf("search did not return reference item %s: %+v", ref.Ref, items)
	}
}
