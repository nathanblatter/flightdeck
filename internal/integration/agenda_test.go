package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

func ptrTime(t time.Time) *time.Time { return &t }

// due_at round-trips through create/update, and update honors the three
// states: absent keeps, a value replaces, clear_due_at wipes.
func TestDueAtRoundTrip(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "life")
	due := time.Now().Add(72 * time.Hour).Truncate(time.Second)

	it := mkItem(t, svc, store.CreateItemParams{ProjectID: p.ID, Title: "flight", DueAt: &due}, "t")
	if it.DueAt == nil || !it.DueAt.Equal(due) {
		t.Fatalf("create due_at = %v, want %v", it.DueAt, due)
	}
	// Absent keeps it.
	it, err := svc.UpdateItem(ctx, store.UpdateItemParams{ID: it.ID, Title: ptr("flight home")}, "t")
	if err != nil || it.DueAt == nil || !it.DueAt.Equal(due) {
		t.Fatalf("absent due_at should keep: %v %v", it.DueAt, err)
	}
	// A value replaces it.
	later := due.Add(24 * time.Hour)
	it, err = svc.UpdateItem(ctx, store.UpdateItemParams{ID: it.ID, DueAt: &later}, "t")
	if err != nil || it.DueAt == nil || !it.DueAt.Equal(later) {
		t.Fatalf("due_at should replace: %v %v", it.DueAt, err)
	}
	// Clear wipes it.
	it, err = svc.UpdateItem(ctx, store.UpdateItemParams{ID: it.ID, ClearDueAt: true}, "t")
	if err != nil || it.DueAt != nil {
		t.Fatalf("clear_due_at should wipe: %v %v", it.DueAt, err)
	}
	// list_items due_before sees dated rows only.
	_ = mkItem(t, svc, store.CreateItemParams{ProjectID: p.ID, Title: "dated", DueAt: &due}, "t")
	rows, err := st.ListItems(ctx, store.ListItemsParams{DueBefore: ptrTime(due.Add(time.Hour))})
	if err != nil || len(rows) != 1 || rows[0].Title != "dated" {
		t.Fatalf("due_before filter: %d rows, err %v", len(rows), err)
	}
}

// The agenda splits overdue from upcoming, flags expired only when nothing was
// logged after the due date, and ignores closed items and paused projects.
func TestAgendaSplitsAndFlagsExpired(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	active := mkProject(t, st, "trips")
	paused := mkProject(t, st, "old")
	pausedStatus := "paused"
	if _, err := st.UpdateProject(ctx, store.UpdateProjectParams{Slug: paused.Slug, Status: &pausedStatus}); err != nil {
		t.Fatalf("pause project: %v", err)
	}
	now := time.Now()
	expired := mkItem(t, svc, store.CreateItemParams{ProjectID: active.ID, Title: "expired offer", DueAt: ptrTime(now.Add(-48 * time.Hour))}, "t")
	touched := mkItem(t, svc, store.CreateItemParams{ProjectID: active.ID, Title: "overdue but noted", DueAt: ptrTime(now.Add(-24 * time.Hour))}, "t")
	soon := mkItem(t, svc, store.CreateItemParams{ProjectID: active.ID, Title: "flight", DueAt: ptrTime(now.Add(48 * time.Hour))}, "t")
	_ = mkItem(t, svc, store.CreateItemParams{ProjectID: active.ID, Title: "far", DueAt: ptrTime(now.Add(30 * 24 * time.Hour))}, "t")
	_ = mkItem(t, svc, store.CreateItemParams{ProjectID: active.ID, Title: "undated"}, "t")
	done := "done"
	_ = mkItem(t, svc, store.CreateItemParams{ProjectID: active.ID, Title: "closed", Status: &done, DueAt: ptrTime(now.Add(-time.Hour))}, "t")
	_ = mkItem(t, svc, store.CreateItemParams{ProjectID: paused.ID, Title: "paused project item", DueAt: ptrTime(now.Add(-time.Hour))}, "t")

	// The creation activity predates the due date for both overdue items (they
	// were created now, after due) — so backdate the created rows, then log a
	// fresh note on `touched` only.
	for _, id := range []any{expired.ID, touched.ID} {
		if _, err := st.Pool.Exec(ctx, `UPDATE activity SET created_at = now() - interval '10 days' WHERE item_id = $1`, id); err != nil {
			t.Fatalf("backdate: %v", err)
		}
	}
	// Orient's proxy for "nothing since the due date" is updated_at (activity
	// rows aren't loaded per item there); the expired item was last edited
	// before it fell due.
	// (The items_set_updated_at trigger would stamp now() over the backdate.)
	for _, q := range []string{
		`ALTER TABLE items DISABLE TRIGGER items_set_updated_at`,
		`UPDATE items SET updated_at = now() - interval '10 days' WHERE id = '` + expired.ID.String() + `'`,
		`ALTER TABLE items ENABLE TRIGGER items_set_updated_at`,
	} {
		if _, err := st.Pool.Exec(ctx, q); err != nil {
			t.Fatalf("backdate item: %v", err)
		}
	}
	if _, err := svc.LogActivity(ctx, store.CreateActivityParams{
		ProjectID: active.ID, ItemID: pgtype.UUID{Bytes: touched.ID, Valid: true}, Kind: ptr("comment"), Actor: ptr("t"), Body: ptr("still negotiating"),
	}); err != nil {
		t.Fatalf("log: %v", err)
	}

	a, err := svc.Agenda(ctx, 7)
	if err != nil {
		t.Fatalf("agenda: %v", err)
	}
	if a.Days != 7 {
		t.Fatalf("days = %d", a.Days)
	}
	if len(a.Overdue) != 2 || a.Overdue[0].Ref != expired.Ref || a.Overdue[1].Ref != touched.Ref {
		t.Fatalf("overdue = %+v", a.Overdue)
	}
	if !a.Overdue[0].Expired || !a.Overdue[0].Overdue {
		t.Fatalf("expired item not flagged: %+v", a.Overdue[0])
	}
	if a.Overdue[1].Expired {
		t.Fatalf("touched item wrongly expired: %+v", a.Overdue[1])
	}
	if len(a.Upcoming) != 1 || a.Upcoming[0].Ref != soon.Ref || a.Upcoming[0].Project != "trips" {
		t.Fatalf("upcoming = %+v", a.Upcoming)
	}
	if len(a.Nudges) != 1 || !strings.Contains(a.Nudges[0], expired.Ref) || strings.Contains(a.Nudges[0], touched.Ref) {
		t.Fatalf("nudges = %v", a.Nudges)
	}
	// Days are clamped and the far item appears inside a wide horizon.
	wide, err := svc.Agenda(ctx, 1000)
	if err != nil || wide.Days != 90 || len(wide.Upcoming) != 2 {
		t.Fatalf("wide agenda: days %d upcoming %d err %v", wide.Days, len(wide.Upcoming), err)
	}
	zero, err := svc.Agenda(ctx, 0)
	if err != nil || zero.Days != 7 {
		t.Fatalf("default days: %d err %v", zero.Days, err)
	}

	// The global map leads with the agenda.
	g, err := svc.GlobalContext(ctx, service.VerbosityCompact)
	if err != nil {
		t.Fatalf("global: %v", err)
	}
	if g.Agenda == nil || len(g.Agenda.Overdue) != 2 || len(g.Agenda.Upcoming) != 1 {
		t.Fatalf("global agenda = %+v", g.Agenda)
	}

	// Orient on the project nudges about the expired item by ref; the touched
	// one is listed as overdue (its edit bumped updated_at past the due date).
	pc, err := svc.ProjectContext(ctx, "trips", service.VerbosityCompact)
	if err != nil {
		t.Fatalf("project context: %v", err)
	}
	joined := strings.Join(pc.Nudges, "\n")
	if !strings.Contains(joined, "expired?") || !strings.Contains(joined, expired.Ref) {
		t.Fatalf("nudges missing expired ref: %v", pc.Nudges)
	}
	var due *time.Time
	for _, it := range pc.OpenItems {
		if it.Ref == soon.Ref {
			due = it.DueAt
		}
	}
	if due == nil {
		t.Fatalf("open items lost due_at")
	}
}

// An agenda with nothing dated keeps the global payload free of the block.
func TestAgendaOmittedWhenNothingDated(t *testing.T) {
	st, svc := setup(t)
	p := mkProject(t, st, "code")
	_ = mkItem(t, svc, store.CreateItemParams{ProjectID: p.ID, Title: "undated"}, "t")
	g, err := svc.GlobalContext(context.Background(), service.VerbosityCompact)
	if err != nil {
		t.Fatalf("global: %v", err)
	}
	if g.Agenda != nil {
		t.Fatalf("agenda should be omitted, got %+v", g.Agenda)
	}
}

func TestParseDue(t *testing.T) {
	if v, err := service.ParseDue(""); err != nil || v != nil {
		t.Fatalf("empty: %v %v", v, err)
	}
	if v, err := service.ParseDue("2026-11-28T15:04:05Z"); err != nil || v == nil || v.UTC().Hour() != 15 {
		t.Fatalf("rfc3339: %v %v", v, err)
	}
	v, err := service.ParseDue("2026-11-28")
	if err != nil || v == nil {
		t.Fatalf("date-only: %v %v", v, err)
	}
	if v.Hour() != 23 || v.Minute() != 59 || v.Second() != 59 || v.Day() != 28 {
		t.Fatalf("date-only should be end of day: %v", v)
	}
	if _, err := service.ParseDue("next tuesday"); err == nil {
		t.Fatalf("garbage should fail")
	}
}
