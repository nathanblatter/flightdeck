package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"text/tabwriter"
	"time"

	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

// runSearchEval replays the last N days of logged searches against the
// current engine and prints a before/after per-tier table, so a ranking or
// recall change is judged on the queries agents actually sent rather than on
// hand-picked examples. Nothing is logged: SearchEval bypasses search_log.
// Needs the same DATABASE_URL / OPENAI_API_KEY / FLIGHTDECK_REDIS_URL as serve
// for the semantic tier to take part.
func runSearchEval(args []string) {
	fs := flag.NewFlagSet("search-eval", flag.ExitOnError)
	days := fs.Int("days", 30, "replay searches logged in the last N days")
	_ = fs.Parse(args)

	ctx := context.Background()
	st, err := store.NewStore(ctx, mustDatabaseURL())
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()
	svc := service.New(st)

	rows, err := st.RecentSearchLog(ctx, time.Now().AddDate(0, 0, -*days))
	if err != nil {
		log.Fatalf("search log: %v", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "query\tbefore fts/any/sem/tri=ret\tafter fts/any/sem/tri=ret\tbest_dist\tchange")
	var zeroBefore, zeroAfter, rescued, lost int
	for _, r := range rows {
		items, _, tiers, err := svc.SearchEval(ctx, r.Query, 0, 0)
		if err != nil {
			log.Printf("%q: %v", r.Query, err)
			continue
		}
		dist := "-"
		if tiers.BestSemanticDistance != nil {
			dist = fmt.Sprintf("%.3f", *tiers.BestSemanticDistance)
		}
		change := ""
		switch {
		case r.Returned == 0 && len(items) > 0:
			change = "RESCUED"
			rescued++
		case r.Returned > 0 && len(items) == 0:
			change = "LOST"
			lost++
		}
		if r.Returned == 0 {
			zeroBefore++
		}
		if len(items) == 0 {
			zeroAfter++
		}
		fmt.Fprintf(tw, "%q\t%d/%d/%d/%d=%d\t%d/%d/%d/%d=%d\t%s\t%s\n",
			r.Query,
			r.FtsHits, r.FtsAnyHits, r.SemanticHits, r.TrigramHits, r.Returned,
			tiers.FTS, tiers.FTSAny, tiers.Semantic, tiers.Trigram, len(items),
			dist, change)
	}
	_ = tw.Flush()
	fmt.Printf("\n%d searches: zero-result %d → %d (rescued %d, lost %d)\n", len(rows), zeroBefore, zeroAfter, rescued, lost)
}
