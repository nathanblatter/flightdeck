package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

func TestRecordContextImpactIsIdempotent(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	project := mkProject(t, st, "alpha")
	item := mkItem(t, svc, store.CreateItemParams{ProjectID: project.ID, Title: "measure"}, "tester")
	itemRef := item.Ref
	key := "session-effect-1"
	delta := int32(15)
	input := service.ContextImpactInput{
		SessionID: " session-1 ",
		Project:   " alpha ",
		Item:      &itemRef,
		Effect:    "helpful",
		Mechanism: "prevented_error",
		ContextRefs: []string{
			" alpha instructions ",
			"alpha-1",
		},
		Evidence:              " the constraint prevented a wrong edit ",
		EstimatedMinutesDelta: &delta,
		IdempotencyKey:        &key,
	}

	first, created, err := svc.RecordContextImpact(ctx, input, "tester")
	if err != nil || !created {
		t.Fatalf("first record = %+v, created %v, err %v", first, created, err)
	}
	second, created, err := svc.RecordContextImpact(ctx, input, "tester")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("replay = %+v, created %v, err %v", second, created, err)
	}
	if first.SessionID != "session-1" || first.Project != "alpha" ||
		first.Evidence != "the constraint prevented a wrong edit" ||
		len(first.ContextRefs) != 2 || first.ContextRefs[0] != "alpha instructions" {
		t.Fatalf("input was not normalized: %+v", first)
	}
	if n := countRows(t, st, `SELECT count(*) FROM context_impact_events`); n != 1 {
		t.Fatalf("impact rows = %d, want 1", n)
	}
}

func TestRecordContextImpactRejectsCrossProjectItem(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	alpha := mkProject(t, st, "alpha")
	beta := mkProject(t, st, "beta")
	item := mkItem(t, svc, store.CreateItemParams{ProjectID: beta.ID, Title: "other"}, "tester")
	itemRef := item.Ref

	_, _, err := svc.RecordContextImpact(ctx, service.ContextImpactInput{
		SessionID: "session-1", Project: alpha.Slug, Item: &itemRef,
		Effect: "helpful", Mechanism: "decision_changed", Evidence: "used context",
	}, "tester")
	if !errors.Is(err, service.ErrInvalidContextImpact) {
		t.Fatalf("cross-project item error = %v, want ErrInvalidContextImpact", err)
	}
}

func TestListContextImpactEventsFiltersAndOrders(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	mkProject(t, st, "alpha")
	mkProject(t, st, "beta")

	alpha, _, err := svc.RecordContextImpact(ctx, service.ContextImpactInput{
		SessionID: "alpha-session", Project: "alpha", Effect: "neutral",
		Mechanism: "ignored", Evidence: "not relevant",
	}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RecordContextImpact(ctx, service.ContextImpactInput{
		SessionID: "beta-session", Project: "beta", Effect: "helpful",
		Mechanism: "reconstruction_saved", Evidence: "loaded prior state",
	}, "tester"); err != nil {
		t.Fatal(err)
	}
	newest, _, err := svc.RecordContextImpact(ctx, service.ContextImpactInput{
		SessionID: "alpha-session-2", Project: "alpha", Effect: "harmful",
		Mechanism: "stale_or_incorrect", Evidence: "old claim",
	}, "tester")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx,
		`UPDATE context_impact_events SET recorded_at = $1 WHERE id = $2`,
		time.Now().Add(-time.Hour), alpha.ID); err != nil {
		t.Fatal(err)
	}

	rows, err := st.ListContextImpactEvents(ctx, store.ListContextImpactEventsParams{
		RecordedAt: time.Now().Add(-24 * time.Hour),
		Project:    ptr("alpha"),
		Lim:        10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != newest.ID || rows[1].ID != alpha.ID {
		t.Fatalf("filtered rows = %+v", rows)
	}
}
