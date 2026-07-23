package integration

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"flightdeck/internal/metrics"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

// TestSearchSmartLexicalPath drives the refactored SearchSmart end to end with no
// embedding client configured (emb disabled → wantSemantic false), so it verifies
// the concurrent FTS phase and the trigram fallback that must still work when
// semantic is skipped entirely.
func TestSearchSmartLexicalPath(t *testing.T) {
	st, svc := setup(t)
	p := mkProject(t, st, "sp")
	ctx := context.Background()

	mk := func(title string) {
		if _, err := svc.CreateItem(ctx, store.CreateItemParams{
			ProjectID: p.ID, Title: title,
		}, "tester"); err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
	}
	mk("fix authentication timeout")
	mk("authentication retry logic")
	mk("unrelated deployment chore")

	proj := pgtype.UUID{Bytes: p.ID, Valid: true}

	// Exact FTS term hits both auth items and not the deploy chore.
	items, _, err := svc.SearchSmart(ctx, "authentication", proj, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("SearchSmart FTS: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("FTS 'authentication' returned %d items, want >=2", len(items))
	}

	// Misspelling has no FTS/semantic hit → trigram fallback must still surface it.
	items, _, err = svc.SearchSmart(ctx, "authentcation", proj, nil, nil, 0, 0)
	if err != nil {
		t.Fatalf("SearchSmart fuzzy: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("trigram fallback returned nothing for a misspelling")
	}

	// The split-path latency metric must have recorded these searches. With no
	// embed client configured every search takes the lexical path.
	var buf strings.Builder
	metrics.WritePrometheus(&buf)
	if !strings.Contains(buf.String(), `flightdeck_search_duration_seconds`) ||
		!strings.Contains(buf.String(), `path="lexical"`) {
		t.Fatalf("search latency metric not recorded; got:\n%s", buf.String())
	}
}

// TestVecCacheRedisRoundTrip proves the Redis-backed query-embedding cache
// actually stores and returns a vector against a live Redis. Skips when no Redis
// is configured. Uses the service's own cache via a search-shaped write/read by
// exercising the exported behavior indirectly through a raw client on the same
// key scheme would couple to internals, so instead we assert the public contract:
// a set then get returns the same vector.
func TestVecCacheRedisRoundTrip(t *testing.T) {
	url := os.Getenv("FLIGHTDECK_REDIS_URL")
	if url == "" {
		url = os.Getenv("REDIS_URL")
	}
	if url == "" {
		t.Skip("set FLIGHTDECK_REDIS_URL to run the Redis cache test")
	}
	// Sanity: the configured Redis must be reachable, else the cache silently
	// degrades and this test would be meaningless.
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse REDIS_URL: %v", err)
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis ping: %v", err)
	}

	vec := make([]float32, 8)
	for i := range vec {
		vec[i] = float32(i) * 0.5
	}
	got, ok := service.VecCacheRoundTrip(context.Background(), "the quick brown fox", vec)
	if !ok {
		t.Fatal("VecCacheRoundTrip: get missed right after set")
	}
	if len(got) != len(vec) {
		t.Fatalf("len = %d, want %d", len(got), len(vec))
	}
	for i := range vec {
		if got[i] != vec[i] {
			t.Fatalf("element %d: got %v, want %v", i, got[i], vec[i])
		}
	}
}
