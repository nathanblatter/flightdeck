package integration

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"flightdeck/internal/store"
)

// The any-term tier rescues multi-word natural-language queries that AND-FTS
// zeroes, but its coverage floor keeps never-filed topics returning nothing.
func TestSearchAnyTermTier(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	if _, err := st.Pool.Exec(ctx, `TRUNCATE search_log`); err != nil {
		t.Fatal(err)
	}
	p := mkProject(t, st, "any")
	mk := func(title string) {
		t.Helper()
		if _, err := svc.CreateItem(ctx, store.CreateItemParams{ProjectID: p.ID, Title: title}, "tester"); err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
	}
	mk("macbook laptop setup checklist")
	mk("sharing relay rendezvous design")
	mk("renew vehicle registration")
	proj := pgtype.UUID{Bytes: p.ID, Valid: true}

	search := func(q string) int {
		t.Helper()
		items, _, err := svc.SearchSmart(ctx, q, proj, nil, nil, 0, 0)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		return len(items)
	}

	// 3 lexemes, 2 present on the item: AND-FTS misses, the any tier rescues.
	if n := search("macbook laptop notes"); n != 1 {
		t.Fatalf("2-of-3 query returned %d items, want 1", n)
	}
	// 1 shared word out of 6: below the floor, and must not be noise-rescued.
	if n := search("gartner options contracts analysis for laptop"); n != 0 {
		t.Fatalf("1-of-6 query returned %d items, want 0", n)
	}
	// Never-filed topic sharing nothing: still nothing, and no trigram
	// fallback either (multi-word queries are not trigram candidates).
	if n := search("quarterly tax estimate"); n != 0 {
		t.Fatalf("never-filed query returned %d items, want 0", n)
	}
	// 2 lexemes, 1 present: a 2-term query clears on half coverage.
	if n := search("relay protocol"); n != 1 {
		t.Fatalf("1-of-2 query returned %d items, want 1", n)
	}
	// Exact AND hit still ranks first when both tiers fire.
	items, _, err := svc.SearchSmart(ctx, "vehicle registration laptop", proj, nil, nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 || items[0].Title != "renew vehicle registration" {
		t.Fatalf("2-of-3 exact-ish ranking = %+v", items)
	}

	// The log records the rescue and the usage report rolls it up.
	var ftsHits, anyHits int
	if err := st.Pool.QueryRow(ctx, `SELECT fts_hits, fts_any_hits FROM search_log WHERE query = 'macbook laptop notes'`).Scan(&ftsHits, &anyHits); err != nil {
		t.Fatal(err)
	}
	if ftsHits != 0 || anyHits != 1 {
		t.Fatalf("search_log fts/any = %d/%d, want 0/1", ftsHits, anyHits)
	}
	rep, err := svc.UsageReport(ctx, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	// macbook, relay, and vehicle (AND missed on "laptop") were all rescues.
	if rep.Search.FtsAnyRescues != 3 {
		t.Fatalf("fts_any_rescues = %d, want 3", rep.Search.FtsAnyRescues)
	}
	// SearchEval runs the same engine without logging.
	var before int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM search_log`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	_, _, tiers, err := svc.SearchEval(ctx, "macbook laptop notes", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var after int
	if err := st.Pool.QueryRow(ctx, `SELECT count(*) FROM search_log`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before || tiers.FTSAny != 1 || tiers.BestSemanticDistance != nil {
		t.Fatalf("eval logged (%d→%d) or tiers wrong: %+v", before, after, tiers)
	}
}
