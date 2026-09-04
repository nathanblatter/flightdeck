package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"flightdeck/internal/api"
	"flightdeck/internal/auth"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

func setupContextImpactHTTP(t *testing.T) (*httptest.Server, *store.Store, *service.Service, string) {
	t.Helper()
	st, svc := setup(t)
	if _, err := st.Pool.Exec(context.Background(), `TRUNCATE api_keys, settings`); err != nil {
		t.Fatalf("truncate keys/settings: %v", err)
	}
	raw := "fd_test_context_impact_key"
	if _, err := st.CreateAPIKey(context.Background(), store.CreateAPIKeyParams{
		Name: "test-agent", KeyHash: auth.HashKey(raw),
		Scopes: []string{auth.ScopeRead, auth.ScopeWrite},
	}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", api.New(st, svc).Routes())
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts, st, svc, raw
}

func TestRecordContextImpactIsIdempotent(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	project := mkProject(t, st, "alpha")
	item := mkItem(t, svc, store.CreateItemParams{ProjectID: project.ID, Title: "measure"}, "tester")
	itemRef := item.Ref
	activityBefore := countRows(t, st, `SELECT count(*) FROM activity`)
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
	if n := countRows(t, st, `SELECT count(*) FROM activity`); n != activityBefore {
		t.Fatalf("activity rows = %d, want unchanged count %d", n, activityBefore)
	}

	if _, err := st.Pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, project.ID); err != nil {
		t.Fatal(err)
	}
	third, created, err := svc.RecordContextImpact(ctx, input, "tester")
	if err != nil || created || third.ID != first.ID {
		t.Fatalf("replay after source deletion = %+v, created %v, err %v", third, created, err)
	}
}

func TestRecordContextImpactRejectsChangedIdempotentPayload(t *testing.T) {
	st, svc := setup(t)
	mkProject(t, st, "alpha")
	key := "stable-key"
	input := service.ContextImpactInput{
		SessionID: "session-1", Project: "alpha", Effect: "neutral",
		Mechanism: "ignored", Evidence: "not relevant", IdempotencyKey: &key,
	}
	if _, _, err := svc.RecordContextImpact(context.Background(), input, "tester"); err != nil {
		t.Fatal(err)
	}
	input.Evidence = "a different outcome"
	if _, _, err := svc.RecordContextImpact(context.Background(), input, "tester"); !errors.Is(err, service.ErrIdempotencyConflict) {
		t.Fatalf("changed replay error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestRecordContextImpactRequiresAuthenticatedActor(t *testing.T) {
	st, svc := setup(t)
	mkProject(t, st, "alpha")
	input := service.ContextImpactInput{
		SessionID: "session-1", Project: "alpha", Effect: "neutral",
		Mechanism: "ignored", Evidence: "not relevant",
	}
	for _, actor := range []string{"", "  "} {
		if _, _, err := svc.RecordContextImpact(context.Background(), input, actor); !errors.Is(err, service.ErrInvalidContextImpact) {
			t.Fatalf("actor %q error = %v, want ErrInvalidContextImpact", actor, err)
		}
	}
}

func TestRecordContextImpactConcurrentRetryCreatesOneEvent(t *testing.T) {
	st, svc := setup(t)
	mkProject(t, st, "alpha")
	key := "concurrent-key"
	input := service.ContextImpactInput{
		SessionID: "session-1", Project: "alpha", Effect: "helpful",
		Mechanism: "reconstruction_saved", Evidence: "loaded prior state", IdempotencyKey: &key,
	}

	const attempts = 8
	var wg sync.WaitGroup
	results := make(chan struct {
		id      string
		created bool
		err     error
	}, attempts)
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event, created, err := svc.RecordContextImpact(context.Background(), input, "tester")
			results <- struct {
				id      string
				created bool
				err     error
			}{event.ID.String(), created, err}
		}()
	}
	wg.Wait()
	close(results)

	createdCount := 0
	var eventID string
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if eventID == "" {
			eventID = result.id
		} else if result.id != eventID {
			t.Fatalf("concurrent retry returned event %s, want %s", result.id, eventID)
		}
		if result.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count = %d, want 1", createdCount)
	}
	if n := countRows(t, st, `SELECT count(*) FROM context_impact_events`); n != 1 {
		t.Fatalf("impact rows = %d, want 1", n)
	}
}

func TestRecordContextImpactConcurrentChangedPayloadConflicts(t *testing.T) {
	st, svc := setup(t)
	mkProject(t, st, "alpha")
	key := "concurrent-changed-key"
	inputs := []service.ContextImpactInput{
		{
			SessionID: "session-1", Project: "alpha", Effect: "helpful",
			Mechanism: "decision_changed", Evidence: "changed the plan", IdempotencyKey: &key,
		},
		{
			SessionID: "session-1", Project: "alpha", Effect: "harmful",
			Mechanism: "stale_or_incorrect", Evidence: "the context was stale", IdempotencyKey: &key,
		},
	}

	start := make(chan struct{})
	results := make(chan error, len(inputs))
	var wg sync.WaitGroup
	for _, input := range inputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := svc.RecordContextImpact(context.Background(), input, "tester")
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, service.ErrIdempotencyConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent result: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes = %d, conflicts = %d; want 1 each", successes, conflicts)
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

func TestContextImpactHTTPContract(t *testing.T) {
	ts, st, svc, key := setupContextImpactHTTP(t)
	alpha := mkProject(t, st, "alpha")
	beta := mkProject(t, st, "beta")
	alphaItem := mkItem(t, svc, store.CreateItemParams{ProjectID: alpha.ID, Title: "alpha item"}, "tester")
	betaItem := mkItem(t, svc, store.CreateItemParams{ProjectID: beta.ID, Title: "beta item"}, "tester")

	payload := map[string]any{
		"session_id": "session-1", "project": "alpha", "item": alphaItem.Ref,
		"effect": "helpful", "mechanism": "prevented_error",
		"context_refs":            []string{"alpha instructions", alphaItem.Ref},
		"evidence":                "the constraint prevented a wrong edit",
		"estimated_minutes_delta": 15, "idempotency_key": "effect-1",
	}
	resp, body := doJSON(t, http.MethodPost, ts.URL+"/api/context-impact", key, payload, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d: %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "idempotency_key") || strings.Contains(string(body), "request_fingerprint") {
		t.Fatalf("create response exposed retry internals: %s", body)
	}
	var created store.ContextImpactEvent
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.Actor != "test-agent" || created.Item == nil || *created.Item != alphaItem.Ref {
		t.Fatalf("created event = %+v", created)
	}

	resp, body = doJSON(t, http.MethodPost, ts.URL+"/api/context-impact", key, payload, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay: status %d: %s", resp.StatusCode, body)
	}
	var replay store.ContextImpactEvent
	if err := json.Unmarshal(body, &replay); err != nil || replay.ID != created.ID {
		t.Fatalf("replay = %+v, err %v", replay, err)
	}
	payload["evidence"] = "different evidence"
	if resp, _ := doJSON(t, http.MethodPost, ts.URL+"/api/context-impact", key, payload, nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("changed replay status = %d, want 409", resp.StatusCode)
	}
	payload["evidence"] = "the constraint prevented a wrong edit"

	badPair := map[string]any{
		"session_id": "bad", "project": "alpha", "effect": "harmful",
		"mechanism": "prevented_error", "evidence": "invalid pair",
	}
	if resp, _ := doJSON(t, http.MethodPost, ts.URL+"/api/context-impact", key, badPair, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad pair status = %d, want 400", resp.StatusCode)
	}
	unknownProject := map[string]any{
		"session_id": "missing", "project": "missing", "effect": "neutral",
		"mechanism": "ignored", "evidence": "not found",
	}
	if resp, _ := doJSON(t, http.MethodPost, ts.URL+"/api/context-impact", key, unknownProject, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project status = %d, want 404", resp.StatusCode)
	}
	unknownItem := map[string]any{
		"session_id": "missing-item", "project": "alpha", "item": "alpha-999",
		"effect": "neutral", "mechanism": "ignored", "evidence": "not found",
	}
	if resp, _ := doJSON(t, http.MethodPost, ts.URL+"/api/context-impact", key, unknownItem, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown item status = %d, want 404", resp.StatusCode)
	}
	crossProject := map[string]any{
		"session_id": "wrong-project", "project": "alpha", "item": betaItem.Ref,
		"effect": "neutral", "mechanism": "ignored", "evidence": "wrong project",
	}
	if resp, _ := doJSON(t, http.MethodPost, ts.URL+"/api/context-impact", key, crossProject, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cross-project item status = %d, want 400", resp.StatusCode)
	}

	if _, _, err := svc.RecordContextImpact(context.Background(), service.ContextImpactInput{
		SessionID: "beta-session", Project: "beta", Effect: "neutral",
		Mechanism: "ignored", Evidence: "not relevant",
	}, "test-agent"); err != nil {
		t.Fatal(err)
	}
	resp, body = doJSON(t, http.MethodGet, ts.URL+"/api/context-impact?days=7&project=alpha&limit=10", key, nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d: %s", resp.StatusCode, body)
	}
	var events []store.ContextImpactEvent
	if err := json.Unmarshal(body, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Project != "alpha" || events[0].ID != created.ID {
		t.Fatalf("filtered events = %+v", events)
	}

	for _, query := range []string{"days=0", "days=nope", "limit=501"} {
		resp, _ := doJSON(t, http.MethodGet, ts.URL+"/api/context-impact?"+query, key, nil, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("query %q status = %d, want 400", query, resp.StatusCode)
		}
	}
}

func TestContextImpactHTTPEnforcesRouteScopes(t *testing.T) {
	ts, st, _, _ := setupContextImpactHTTP(t)
	mkProject(t, st, "alpha")
	readKey := "fd_test_context_impact_read_key"
	if _, err := st.CreateAPIKey(context.Background(), store.CreateAPIKeyParams{
		Name: "read-agent", KeyHash: auth.HashKey(readKey), Scopes: []string{auth.ScopeRead},
	}); err != nil {
		t.Fatal(err)
	}

	payload := map[string]any{
		"session_id": "session-1", "project": "alpha", "effect": "neutral",
		"mechanism": "ignored", "evidence": "not relevant",
	}
	if resp, _ := doJSON(t, http.MethodPost, ts.URL+"/api/context-impact", readKey, payload, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-only POST status = %d, want 403", resp.StatusCode)
	}
	if resp, _ := doJSON(t, http.MethodGet, ts.URL+"/api/context-impact", readKey, nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("read-only GET status = %d, want 200", resp.StatusCode)
	}
}
