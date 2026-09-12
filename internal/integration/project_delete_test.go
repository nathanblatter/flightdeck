package integration

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

// TestArchiveHidesProject checks the reversible half: archiving takes a project
// out of the default listing without destroying anything, and restoring brings
// it back. This is what makes archive a real state rather than a label.
func TestArchiveHidesProject(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "archive-me")
	mkItem(t, svc, store.CreateItemParams{ProjectID: p.ID, Title: "keep me", Type: strp("task")}, "test")

	if err := svc.ArchiveProject(ctx, p.Slug); err != nil {
		t.Fatalf("archive: %v", err)
	}

	listed, err := st.ListProjects(ctx, nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, got := range listed {
		if got.Slug == p.Slug {
			t.Fatal("archived project still appears in the default project list")
		}
	}
	// ...but is still there when asked for by status, with its items intact.
	archived := "archived"
	if listed, err = st.ListProjects(ctx, &archived); err != nil {
		t.Fatalf("list archived: %v", err)
	}
	if len(listed) != 1 || listed[0].Slug != p.Slug {
		t.Fatalf("archived project not listed by status filter: %+v", listed)
	}
	if n := countRows(t, st, `SELECT count(*) FROM items WHERE project_id = $1`, p.ID); n != 1 {
		t.Fatalf("archive destroyed items: got %d, want 1", n)
	}
}

// TestArchiveHidesItemsFromBoard covers the follow-on to hiding the project: an
// archived project's items must leave the unfiltered board too. Otherwise
// archiving doesn't declutter anything, and the orphaned cards render without a
// project chip because the project is no longer in the listing that resolves it.
func TestArchiveHidesItemsFromBoard(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	archivedP := mkProject(t, st, "archived-proj")
	liveP := mkProject(t, st, "live-proj")
	hidden := mkItem(t, svc, store.CreateItemParams{ProjectID: archivedP.ID, Title: "hidden", Type: strp("task")}, "test")
	shown := mkItem(t, svc, store.CreateItemParams{ProjectID: liveP.ID, Title: "shown", Type: strp("task")}, "test")

	if err := svc.ArchiveProject(ctx, archivedP.Slug); err != nil {
		t.Fatalf("archive: %v", err)
	}

	all, err := st.ListItems(ctx, store.ListItemsParams{})
	if err != nil {
		t.Fatalf("list items: %v", err)
	}
	for _, it := range all {
		if it.ID == hidden.ID {
			t.Error("item of an archived project still appears on the unfiltered board")
		}
	}
	if !slices.ContainsFunc(all, func(it store.Item) bool { return it.ID == shown.ID }) {
		t.Error("archiving one project hid another project's items")
	}

	// Asking for the archived project explicitly must still return its items —
	// that is what the drawer and a restore-preview rely on.
	byProject, err := st.ListItems(ctx, store.ListItemsParams{ProjectID: pgtype.UUID{Bytes: archivedP.ID, Valid: true}})
	if err != nil {
		t.Fatalf("list items by project: %v", err)
	}
	if len(byProject) != 1 || byProject[0].ID != hidden.ID {
		t.Fatalf("explicit project filter hid the archived project's items: %d rows", len(byProject))
	}
}

// TestPurgeRequiresArchive is the first guard: a purge of a live project is
// refused, so destroying history always takes two deliberate steps.
func TestPurgeRequiresArchive(t *testing.T) {
	st, svc := setup(t)
	p := mkProject(t, st, "still-active")

	_, err := svc.PurgeProject(context.Background(), p.Slug)
	if !errors.Is(err, service.ErrProjectNotEmpty) {
		t.Fatalf("purge of a non-archived project: got %v, want ErrProjectNotEmpty", err)
	}
	if n := countRows(t, st, `SELECT count(*) FROM projects WHERE slug = $1`, p.Slug); n != 1 {
		t.Fatal("refused purge deleted the project anyway")
	}
}

// TestPurgeRefusesWithChildren is the second guard. Without it the parent_slug
// ON DELETE SET NULL would silently re-root the children, restructuring the
// tree as an invisible side effect of a delete.
func TestPurgeRefusesWithChildren(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	parent := mkProject(t, st, "parent")
	child := mkChildProject(t, st, "child", parent.Slug)

	if err := svc.ArchiveProject(ctx, parent.Slug); err != nil {
		t.Fatalf("archive: %v", err)
	}
	_, err := svc.PurgeProject(ctx, parent.Slug)
	if !errors.Is(err, service.ErrProjectNotEmpty) {
		t.Fatalf("purge with children: got %v, want ErrProjectNotEmpty", err)
	}

	// An archived child must also block the purge — it is still a row that the
	// FK would re-root, and it is exactly the case a caller is likely to miss.
	if err := svc.ArchiveProject(ctx, child.Slug); err != nil {
		t.Fatalf("archive child: %v", err)
	}
	if _, err = svc.PurgeProject(ctx, parent.Slug); !errors.Is(err, service.ErrProjectNotEmpty) {
		t.Fatalf("purge with archived child: got %v, want ErrProjectNotEmpty", err)
	}
}

// TestPurgeCascades verifies the happy path actually removes everything the
// project owned, and reports accurate counts back to the caller.
func TestPurgeCascades(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	p := mkProject(t, st, "purge-me")
	keep := mkProject(t, st, "keep-me")

	item := mkItem(t, svc, store.CreateItemParams{ProjectID: p.ID, Title: "doomed", Type: strp("task")}, "test")
	if _, err := svc.LogActivity(ctx, store.CreateActivityParams{
		ProjectID: p.ID, Kind: strp("decision"), Actor: strp("test"), Body: strp("why"),
	}); err != nil {
		t.Fatalf("log activity: %v", err)
	}
	survivor := mkItem(t, svc, store.CreateItemParams{ProjectID: keep.ID, Title: "survivor", Type: strp("task")}, "test")

	if err := svc.ArchiveProject(ctx, p.Slug); err != nil {
		t.Fatalf("archive: %v", err)
	}
	counts, err := svc.PurgeProject(ctx, p.Slug)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if counts.Items != 1 {
		t.Errorf("reported items: got %d, want 1", counts.Items)
	}
	// The item's own 'created' activity plus the decision logged above.
	if counts.Activity != 2 {
		t.Errorf("reported activity: got %d, want 2", counts.Activity)
	}

	if n := countRows(t, st, `SELECT count(*) FROM projects WHERE slug = $1`, p.Slug); n != 0 {
		t.Error("project survived the purge")
	}
	if n := countRows(t, st, `SELECT count(*) FROM items WHERE id = $1`, item.ID); n != 0 {
		t.Error("item survived the purge")
	}
	if n := countRows(t, st, `SELECT count(*) FROM activity WHERE project_id = $1`, p.ID); n != 0 {
		t.Error("activity survived the purge")
	}
	// The blast radius must stop at the project boundary.
	if n := countRows(t, st, `SELECT count(*) FROM items WHERE id = $1`, survivor.ID); n != 1 {
		t.Error("purge deleted an item belonging to another project")
	}
}

func strp(s string) *string { return &s }
