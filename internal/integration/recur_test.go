package integration

import (
	"context"
	"testing"
	"time"

	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

// successorOf returns the item linked relates_to from pred, failing unless
// there is exactly one.
func successorOf(t *testing.T, st *store.Store, pred store.Item) store.Item {
	t.Helper()
	ctx := context.Background()
	links, err := st.ListLinksForItem(ctx, pred.ID)
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	var out []store.Item
	for _, l := range links {
		if l.FromItemID == pred.ID && l.Kind == "relates_to" {
			it, err := st.GetItem(ctx, l.ToItemID)
			if err != nil {
				t.Fatalf("get successor: %v", err)
			}
			out = append(out, it)
		}
	}
	if len(out) != 1 {
		t.Fatalf("want exactly one successor of %s, got %d", pred.Ref, len(out))
	}
	return out[0]
}

// Completing an interval-recurring item spawns the next one, due one interval
// after the predecessor's due date, linked and in todo.
func TestRecurIntervalSpawnsOnComplete(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "life")
	due := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	pred := mkItem(t, svc, store.CreateItemParams{
		ProjectID: p.ID, Title: "4Runner registration", Body: strp("DMV, bring insurance"),
		Priority: strp("high"), Status: strp("todo"), Tags: []string{"car", "dmv"},
		DueAt: &due, Recur: strp("P1Y"),
	}, "tester")

	w, err := svc.CompleteItemResult(ctx, pred.ID, "renewed for 2026", "", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if w.Item.Status != "done" {
		t.Fatalf("predecessor status %q, want done", w.Item.Status)
	}
	if w.Spawned == nil {
		t.Fatal("want a spawned successor")
	}
	if n := w.Nudges(); len(n) != 0 {
		t.Fatalf("interval rule should not nudge, got %v", n)
	}
	succ := successorOf(t, st, w.Item)
	if succ.ID != w.Spawned.ID {
		t.Fatalf("linked successor %s != spawned %s", succ.Ref, w.Spawned.Ref)
	}
	want := time.Date(2027, 10, 31, 12, 0, 0, 0, time.UTC)
	if succ.DueAt == nil || !succ.DueAt.Equal(want) {
		t.Fatalf("successor due %v, want %v", succ.DueAt, want)
	}
	if succ.Status != "todo" || succ.Title != pred.Title || succ.Body != pred.Body ||
		succ.Priority != "high" || succ.Type != pred.Type || succ.Source != "agent" ||
		succ.Recur == nil || *succ.Recur != "P1Y" || len(succ.Tags) != 2 {
		t.Fatalf("successor fields off: %+v", succ)
	}
	if n := countRows(t, st, `SELECT count(*) FROM activity WHERE item_id = $1 AND kind = 'comment' AND body = $2`, succ.ID, "recurrence of "+pred.Ref); n != 1 {
		t.Fatalf("want one 'recurrence of' comment on the successor, got %d", n)
	}
	// The response DTO carries the brief and no nudge.
	out := w.DTO()
	if out.Spawned == nil || out.Spawned.Ref != succ.Ref || out.Spawned.DueAt == nil || len(out.Nudges) != 0 {
		t.Fatalf("dto spawned off: %+v nudges=%v", out.Spawned, out.Nudges)
	}

	// Completing the successor spawns again (chain continues).
	w2, err := svc.CompleteItemResult(ctx, succ.ID, "renewed for 2027", "", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if w2.Spawned == nil {
		t.Fatal("completing the successor should spawn again")
	}
	want2 := time.Date(2028, 10, 31, 12, 0, 0, 0, time.UTC)
	if w2.Spawned.DueAt == nil || !w2.Spawned.DueAt.Equal(want2) {
		t.Fatalf("second successor due %v, want %v", w2.Spawned.DueAt, want2)
	}
	if n := countRows(t, st, `SELECT count(*) FROM items WHERE project_id = $1`, p.ID); n != 3 {
		t.Fatalf("want 3 items in the chain, got %d", n)
	}
}

// A free-text rule spawns a successor with no due date and nudges the caller
// to set one; a predecessor without due_at is fine too.
func TestRecurFreeTextSpawnsWithNudge(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "car")
	rule := "every even year in October"
	pred := mkItem(t, svc, store.CreateItemParams{
		ProjectID: p.ID, Title: "Emissions test", Status: strp("todo"), Recur: &rule,
	}, "tester")

	w, err := svc.CompleteItemResult(ctx, pred.ID, "passed", "", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if w.Spawned == nil {
		t.Fatal("want a spawned successor")
	}
	succ := successorOf(t, st, w.Item)
	if succ.DueAt != nil {
		t.Fatalf("free-text successor should have no due_at, got %v", succ.DueAt)
	}
	if succ.Recur == nil || *succ.Recur != rule {
		t.Fatalf("successor rule %v, want %q", succ.Recur, rule)
	}
	out := w.DTO()
	wantNudge := "recurrence spawned " + succ.Ref + " with no due date — set due_at from the rule: " + rule
	if len(out.Nudges) != 1 || out.Nudges[0] != wantNudge {
		t.Fatalf("nudges = %v, want [%q]", out.Nudges, wantNudge)
	}
}

// update_item moving a recurring item to done spawns once; wontfix ends the
// chain; a done→done re-update does not spawn twice.
func TestRecurUpdateItemIdempotentAndWontfix(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "life")
	due := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	pred := mkItem(t, svc, store.CreateItemParams{
		ProjectID: p.ID, Title: "Oil change", Status: strp("todo"), DueAt: &due, Recur: strp("P6M"),
	}, "tester")

	w, err := svc.UpdateItemResult(ctx, store.UpdateItemParams{ID: pred.ID, Status: strp("done")}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if w.Spawned == nil {
		t.Fatal("update_item → done should spawn")
	}
	want := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	if w.Spawned.DueAt == nil || !w.Spawned.DueAt.Equal(want) {
		t.Fatalf("successor due %v, want %v", w.Spawned.DueAt, want)
	}

	// done → done again: no second successor.
	w2, err := svc.UpdateItemResult(ctx, store.UpdateItemParams{ID: pred.ID, Status: strp("done")}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if w2.Spawned != nil {
		t.Fatalf("done→done re-update spawned %s", w2.Spawned.Ref)
	}
	// Likewise via complete_item on an already-done item.
	w3, err := svc.CompleteItemResult(ctx, pred.ID, "again", "", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if w3.Spawned != nil {
		t.Fatalf("complete_item on a done item spawned %s", w3.Spawned.Ref)
	}
	_ = successorOf(t, st, w.Item) // exactly one
	if n := countRows(t, st, `SELECT count(*) FROM items WHERE project_id = $1`, p.ID); n != 2 {
		t.Fatalf("want 2 items, got %d", n)
	}

	// wontfix ends the chain.
	succ := *w.Spawned
	w4, err := svc.UpdateItemResult(ctx, store.UpdateItemParams{ID: succ.ID, Status: strp("wontfix")}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if w4.Spawned != nil {
		t.Fatalf("wontfix spawned %s", w4.Spawned.Ref)
	}
	if n := countRows(t, st, `SELECT count(*) FROM items WHERE project_id = $1`, p.ID); n != 2 {
		t.Fatalf("want 2 items after wontfix, got %d", n)
	}
}

// Clearing / replacing the rule through UpdateItem's three-way convention, and
// completing an item whose rule was cleared spawns nothing. Also: a rule on a
// predecessor with no due date bases the successor on now.
func TestRecurClearAndNoDueBase(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "life")
	a := mkItem(t, svc, store.CreateItemParams{ProjectID: p.ID, Title: "a", Status: strp("todo"), Recur: strp("P1Y")}, "tester")

	val, clear, err := service.RecurParam(strp(""))
	if err != nil || val != nil || !clear {
		t.Fatalf("RecurParam(\"\") = %v %v %v", val, clear, err)
	}
	cleared, err := svc.UpdateItem(ctx, store.UpdateItemParams{ID: a.ID, Recur: val, ClearRecur: clear}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Recur != nil {
		t.Fatalf("recur not cleared: %v", *cleared.Recur)
	}
	w, err := svc.CompleteItemResult(ctx, a.ID, "done", "", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if w.Spawned != nil {
		t.Fatalf("cleared rule spawned %s", w.Spawned.Ref)
	}

	b := mkItem(t, svc, store.CreateItemParams{ProjectID: p.ID, Title: "b", Status: strp("todo"), Recur: strp("P10D")}, "tester")
	before := time.Now()
	wb, err := svc.CompleteItemResult(ctx, b.ID, "done", "", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if wb.Spawned == nil || wb.Spawned.DueAt == nil {
		t.Fatal("no-due predecessor should still spawn a dated successor")
	}
	lo, hi := before.AddDate(0, 0, 10).Add(-time.Minute), time.Now().AddDate(0, 0, 10).Add(time.Minute)
	if wb.Spawned.DueAt.Before(lo) || wb.Spawned.DueAt.After(hi) {
		t.Fatalf("successor due %v not within now+10d", wb.Spawned.DueAt)
	}
}
