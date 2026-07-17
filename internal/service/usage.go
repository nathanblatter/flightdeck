package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"golang.org/x/sync/errgroup"

	"flightdeck/internal/dto"
	"flightdeck/internal/store"
)

// maxRecordedArgs caps the raw tool-call arguments stored per row; bigger
// payloads are replaced by a size stub so one giant create_items can't bloat
// the analytics table.
const maxRecordedArgs = 2048

// ToolCall is one observed MCP tool invocation, recorded for usage analytics.
type ToolCall struct {
	Tool        string
	Actor       string
	Project     string
	OK          bool
	Err         string
	Duration    time.Duration
	Args        json.RawMessage
	ResultBytes int
}

// RecordToolCall persists one tool invocation. Best-effort: analytics must
// never break or slow the call being measured, so errors are logged and the
// context is detached from the (possibly already finished) request.
func (s *Service) RecordToolCall(ctx context.Context, c ToolCall) {
	args := c.Args
	if len(args) > maxRecordedArgs {
		args = json.RawMessage(fmt.Sprintf(`{"_truncated":true,"_bytes":%d}`, len(c.Args)))
	}
	if len(c.Err) > 500 {
		c.Err = c.Err[:500]
	}
	err := s.St.InsertToolCall(context.WithoutCancel(ctx), store.InsertToolCallParams{
		Tool:        c.Tool,
		Actor:       c.Actor,
		Project:     c.Project,
		Ok:          c.OK,
		Error:       c.Err,
		DurationMs:  int32(c.Duration.Milliseconds()),
		Args:        args,
		ResultBytes: int32(c.ResultBytes),
	})
	if err != nil {
		log.Printf("usage: record tool call: %v", err)
	}
}

// logSearch persists one search's per-tier hit counts (best-effort).
func (s *Service) logSearch(ctx context.Context, actor, query string, ftsN, semN, triN, actN, returned int) {
	err := s.St.InsertSearchLog(context.WithoutCancel(ctx), store.InsertSearchLogParams{
		Actor:        actor,
		Query:        query,
		FtsHits:      int32(ftsN),
		SemanticHits: int32(semN),
		TrigramHits:  int32(triN),
		ActivityHits: int32(actN),
		Returned:     int32(returned),
	})
	if err != nil {
		log.Printf("usage: record search: %v", err)
	}
}

// UsageReport aggregates tool-call and search telemetry over the last `days`
// days. knownTools (the caller's tool registry, optional) is diffed against
// observed usage to surface tools nothing ever calls.
func (s *Service) UsageReport(ctx context.Context, days int, knownTools []string) (dto.UsageReport, error) {
	if days <= 0 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	since := time.Now().AddDate(0, 0, -days)

	var (
		stats    []store.ToolCallStatsRow
		daily    []store.DailyToolCallsRow
		projects []store.TopProjectsByToolCallsRow
		errsRows []store.RecentToolErrorsRow
		search   store.SearchUsageSummaryRow
		zeroQs   []store.RecentZeroResultSearchesRow
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { stats, err = s.St.ToolCallStats(gctx, since); return })
	g.Go(func() (err error) { daily, err = s.St.DailyToolCalls(gctx, since); return })
	g.Go(func() (err error) { projects, err = s.St.TopProjectsByToolCalls(gctx, since); return })
	g.Go(func() (err error) { errsRows, err = s.St.RecentToolErrors(gctx, since); return })
	g.Go(func() (err error) { search, err = s.St.SearchUsageSummary(gctx, since); return })
	g.Go(func() (err error) { zeroQs, err = s.St.RecentZeroResultSearches(gctx, since); return })
	if err := g.Wait(); err != nil {
		return dto.UsageReport{}, err
	}

	rep := dto.UsageReport{
		Days:  days,
		Since: since,
		Tools: make([]dto.ToolUsage, 0, len(stats)),
	}
	used := make(map[string]bool, len(stats))
	for _, r := range stats {
		used[r.Tool] = true
		rep.TotalCalls += int(r.Calls)
		rep.TotalErrors += int(r.Errors)
		rep.Tools = append(rep.Tools, dto.ToolUsage{
			Tool:        r.Tool,
			Calls:       int(r.Calls),
			Errors:      int(r.Errors),
			P50Ms:       r.P50Ms,
			P95Ms:       r.P95Ms,
			AvgResultKB: r.AvgResultBytes / 1024,
			LastUsed:    r.LastUsed,
		})
	}
	for _, name := range knownTools {
		if !used[name] {
			rep.UnusedTools = append(rep.UnusedTools, name)
		}
	}
	for _, d := range daily {
		rep.Daily = append(rep.Daily, dto.DayCalls{
			Day: d.Day.Format("2006-01-02"), Calls: int(d.Calls), Errors: int(d.Errors),
		})
	}
	for _, p := range projects {
		rep.TopProjects = append(rep.TopProjects, dto.ProjectCalls{Project: p.Project, Calls: int(p.Calls)})
	}
	for _, e := range errsRows {
		rep.RecentErrors = append(rep.RecentErrors, dto.ToolError{Tool: e.Tool, Error: e.Error, At: e.CalledAt})
	}
	rep.Search = dto.SearchUsage{
		Searches:        int(search.Searches),
		ZeroResult:      int(search.ZeroResult),
		SemanticRescues: int(search.SemanticRescues),
		TrigramRescues:  int(search.TrigramRescues),
		AvgReturned:     search.AvgReturned,
	}
	for _, z := range zeroQs {
		rep.Search.ZeroResultQueries = append(rep.Search.ZeroResultQueries, z.Query)
	}
	return rep, nil
}
