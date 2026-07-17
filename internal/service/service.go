// Package service holds the small amount of cross-cutting business logic shared
// by the HTTP API and the MCP server: creating an item also appends a `created`
// activity, and a status change appends a `status_change` activity. Keeping it
// here (rather than in handlers) is what makes the activity log trustworthy no
// matter whether a write arrives over REST, MCP, or the kanban board.
package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"flightdeck/internal/auth"
	"flightdeck/internal/dto"
	"flightdeck/internal/embed"
	"flightdeck/internal/pgvec"
	"flightdeck/internal/store"
)

type Service struct {
	St    *store.Store
	hc    *http.Client
	cache *ttlCache
	emb   *embed.Client
}

func New(st *store.Store) *Service {
	return &Service{
		St:    st,
		hc:    &http.Client{Timeout: 10 * time.Second},
		cache: newTTLCache(3 * time.Second),
		emb:   embed.NewFromEnv(),
	}
}

func projectUUID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

func itemUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func strptr(s string) *string { return &s }

// CreateItem inserts an item and appends a `created` activity in one tx. When an
// idempotency_key is supplied, a prior item with that key is returned unchanged
// (no duplicate, no new activity) — making creation safe to retry in autonomous
// loops across interruptions and context compactions.
func (s *Service) CreateItem(ctx context.Context, p store.CreateItemParams, actor string) (store.Item, error) {
	hasKey := p.IdempotencyKey != nil && *p.IdempotencyKey != ""
	idemParams := store.GetItemByIdempotencyKeyParams{ProjectID: p.ProjectID, IdempotencyKey: p.IdempotencyKey}
	if hasKey {
		if existing, err := s.St.GetItemByIdempotencyKey(ctx, idemParams); err == nil {
			return existing, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return store.Item{}, err
		}
	}
	var item store.Item
	err := s.St.WithTx(ctx, func(q *store.Queries) error {
		var err error
		item, err = q.CreateItem(ctx, p)
		if err != nil {
			return err
		}
		_, err = q.CreateActivity(ctx, store.CreateActivityParams{
			ProjectID: item.ProjectID,
			ItemID:    itemUUID(item.ID),
			Kind:      strptr("created"),
			Actor:     strptr(actor),
			Body:      strptr(fmt.Sprintf("created %s: %s", item.Type, item.Title)),
		})
		if err != nil {
			return err
		}
		return s.enqueue(ctx, q, item.ProjectID, "item.created", dto.ToItem(item))
	})
	if err != nil {
		// Lost an idempotency-key race: another caller inserted it first.
		if hasKey && isUniqueViolation(err) {
			if existing, gerr := s.St.GetItemByIdempotencyKey(ctx, idemParams); gerr == nil {
				return existing, nil
			}
		}
		return item, err
	}
	s.cache.clear()
	return item, nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint error.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// UpdateItem applies a patch and, if the status changed, appends a
// `status_change` activity recording the from→to move.
func (s *Service) UpdateItem(ctx context.Context, p store.UpdateItemParams, actor string) (store.Item, error) {
	var item store.Item
	err := s.St.WithTx(ctx, func(q *store.Queries) error {
		prev, err := q.GetItem(ctx, p.ID)
		if err != nil {
			return err
		}
		item, err = q.UpdateItem(ctx, p)
		if errors.Is(err, pgx.ErrNoRows) {
			// The row exists (GetItem above succeeded), so a no-row update means
			// the expected_version guard didn't match: a concurrent edit.
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if p.Status != nil && *p.Status != prev.Status {
			_, err = q.CreateActivity(ctx, store.CreateActivityParams{
				ProjectID: item.ProjectID,
				ItemID:    itemUUID(item.ID),
				Kind:      strptr("status_change"),
				Actor:     strptr(actor),
				Body:      strptr(fmt.Sprintf("%s → %s", prev.Status, item.Status)),
			})
			if err != nil {
				return err
			}
			return s.enqueue(ctx, q, item.ProjectID, "item.status_changed", map[string]any{
				"item": dto.ToItem(item), "from": prev.Status, "to": item.Status,
			})
		}
		return nil
	})
	if err == nil {
		s.cache.clear()
	}
	return item, err
}

// LogActivity appends a free-form activity row (decision/progress/comment) and
// enqueues an activity.logged event in the same transaction.
func (s *Service) LogActivity(ctx context.Context, p store.CreateActivityParams) (store.Activity, error) {
	var row store.Activity
	err := s.St.WithTx(ctx, func(q *store.Queries) error {
		var err error
		row, err = q.CreateActivity(ctx, p)
		if err != nil {
			return err
		}
		return s.enqueue(ctx, q, row.ProjectID, "activity.logged", dto.ToActivity(row))
	})
	if err == nil {
		s.cache.clear()
	}
	return row, err
}

// ProjectContext builds the orient bundle for a single project. Verbosity
// controls body truncation (compact for agents, full for the UI). The dependent
// reads run in parallel, and the result is briefly cached (cleared on any write).
func (s *Service) ProjectContext(ctx context.Context, slug string, v Verbosity) (dto.ProjectContext, error) {
	key := "proj:" + slug + ":" + string(v)
	if cached, ok := s.cache.get(key); ok {
		return cached.(dto.ProjectContext), nil
	}
	p, err := s.St.GetProjectBySlug(ctx, slug)
	if err != nil {
		return dto.ProjectContext{}, err
	}
	itemMax, actMax := bodyLimits(v)

	var (
		open         []store.Item
		recent       []store.Activity
		counts       []store.CountItemsByStatusForProjectRow
		edges        []store.ListBlockingEdgesByProjectRow
		rejected     []store.Activity
		sinceSummary int64
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		open, err = s.St.ListOpenItemsByProject(gctx, store.ListOpenItemsByProjectParams{ProjectID: p.ID, Limit: 50})
		return
	})
	g.Go(func() (err error) {
		recent, err = s.St.ListRecentDecisionsByProject(gctx, store.ListRecentDecisionsByProjectParams{ProjectID: p.ID, Limit: 15})
		return
	})
	g.Go(func() (err error) {
		counts, err = s.St.CountItemsByStatusForProject(gctx, p.ID)
		return
	})
	g.Go(func() (err error) {
		edges, err = s.St.ListBlockingEdgesByProject(gctx, p.ID)
		return
	})
	g.Go(func() (err error) {
		rejected, err = s.St.ListRejectedByProject(gctx, store.ListRejectedByProjectParams{ProjectID: p.ID, Limit: 15})
		return
	})
	g.Go(func() (err error) {
		sinceSummary, err = s.St.CountActivitySince(gctx, store.CountActivitySinceParams{ProjectID: p.ID, CreatedAt: p.UpdatedAt})
		return
	})
	if err := g.Wait(); err != nil {
		return dto.ProjectContext{}, err
	}

	blockedBy := make(map[uuid.UUID][]string, len(edges))
	for _, e := range edges {
		blockedBy[e.BlockedID] = append(blockedBy[e.BlockedID], e.BlockerTitle)
	}
	openItems := dto.ToItemsTrunc(open, itemMax)
	for i := range open {
		if blockers, ok := blockedBy[open[i].ID]; ok {
			openItems[i].Blocked = true
			openItems[i].BlockedBy = blockers
		}
	}
	countMap := make(map[string]int, len(counts))
	for _, c := range counts {
		countMap[c.Status] = int(c.N)
	}
	readyNext, nudges := orientHints(openItems, int(sinceSummary))
	bundle := dto.ProjectContext{
		Project:                dto.ToProject(p),
		OpenItems:              openItems,
		RecentActivity:         dto.ToActivitiesTrunc(recent, actMax),
		Counts:                 countMap,
		ReadyNext:              readyNext,
		Nudges:                 nudges,
		RejectedApproaches:     dto.ToActivitiesTrunc(rejected, actMax),
		SummaryUpdatedAt:       p.UpdatedAt,
		ActivitiesSinceSummary: int(sinceSummary),
	}
	s.cache.set(key, bundle)
	return bundle, nil
}

// orientHints derives the "what's next + housekeeping" layer from the already
// loaded open items, so orient folds in next_action and stale signals without
// extra round trips. openItems must be priority-ordered with Blocked flags set.
func orientHints(openItems []dto.Item, sinceSummary int) (readyNext []dto.ItemBrief, nudges []string) {
	const readyLimit = 3
	staleCutoff := time.Now().Add(-24 * time.Hour)
	var staleRefs []string
	for _, it := range openItems {
		if !it.Blocked && len(readyNext) < readyLimit {
			readyNext = append(readyNext, dto.ToItemBrief(it))
		}
		if it.Status == "in_progress" && it.UpdatedAt.Before(staleCutoff) {
			staleRefs = append(staleRefs, it.Ref)
		}
	}
	if n := len(staleRefs); n > 0 {
		nudges = append(nudges, fmt.Sprintf("%s in progress with no update in 24h+ (%s) — log progress or move it",
			plural(n, "item"), strings.Join(staleRefs, ", ")))
	}
	if sinceSummary >= 10 {
		nudges = append(nudges, fmt.Sprintf("summary may be stale: %d activities since it was last refreshed — consider update_project_summary", sinceSummary))
	}
	return readyNext, nudges
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// GlobalContext builds the horizontal "load the map" view across active
// projects. Top items for every project come from one windowed query (no N+1),
// and the result is briefly cached (cleared on any write).
func (s *Service) GlobalContext(ctx context.Context, v Verbosity) (dto.GlobalContext, error) {
	key := "global:" + string(v)
	if cached, ok := s.cache.get(key); ok {
		return cached.(dto.GlobalContext), nil
	}
	active := "active"
	projects, err := s.St.ListProjects(ctx, &active)
	if err != nil {
		return dto.GlobalContext{}, err
	}
	counts, err := s.countsByProject(ctx)
	if err != nil {
		return dto.GlobalContext{}, err
	}
	rows, err := s.St.ListTopOpenItemsPerProject(ctx, 5)
	if err != nil {
		return dto.GlobalContext{}, err
	}
	itemMax, _ := bodyLimits(v)
	topByProject := make(map[uuid.UUID][]dto.Item, len(projects))
	for _, r := range rows {
		topByProject[r.ProjectID] = append(topByProject[r.ProjectID], dto.TopOpenRowToItem(r, itemMax))
	}
	out := dto.GlobalContext{Projects: make([]dto.ProjectOverview, 0, len(projects))}
	for _, p := range projects {
		out.Projects = append(out.Projects, dto.ProjectOverview{
			Project:  dto.ToProject(p),
			Counts:   counts[p.ID],
			TopItems: topByProject[p.ID],
		})
	}
	s.cache.set(key, out)
	return out, nil
}

// countsByProject returns per-project status→count maps for non-deleted items.
func (s *Service) countsByProject(ctx context.Context) (map[uuid.UUID]map[string]int, error) {
	rows, err := s.St.CountItemsByStatus(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]map[string]int)
	for _, row := range rows {
		m := out[row.ProjectID]
		if m == nil {
			m = make(map[string]int)
			out[row.ProjectID] = m
		}
		m[row.Status] = int(row.N)
	}
	return out, nil
}

// ErrNotFound is returned when a lookup misses (mapped to 404 by handlers).
var ErrNotFound = pgx.ErrNoRows

// ErrConflict is returned when an optimistic-concurrency check fails — the item
// was modified since the caller read it (mapped to 409 by handlers).
var ErrConflict = errors.New("version conflict: item was modified since you read it")

// SearchSmart runs the full search over items and activity, blending lexical
// (FTS) and semantic (vector) recall with reciprocal-rank fusion — so a weak
// lexical hit no longer masks a conceptually better semantic match, and
// decision/progress logs are semantically searchable too. When no blended tier
// matches anything, items fall back to trigram for typos/fragments. The
// semantic tier is skipped (with a log) when embeddings aren't configured or
// the embedding call fails, so search degrades cleanly to lexical-only.
func (s *Service) SearchSmart(ctx context.Context, q string, projectID pgtype.UUID, typ *string, lim *int32, itemMax, actMax int) ([]dto.Item, []dto.Activity, error) {
	// Embed the query once, shared by the item and activity semantic tiers.
	var qvec *pgvec.Vector
	if s.emb.Enabled() {
		if vecs, err := s.emb.Embed(ctx, []string{q}); err != nil {
			log.Printf("search: embedding query failed (lexical-only for this query): %v", err)
		} else if len(vecs) == 1 {
			v := pgvec.New(vecs[0])
			qvec = &v
		}
	}
	items, tiers, err := s.searchItems(ctx, q, qvec, projectID, typ, lim, itemMax)
	if err != nil {
		return nil, nil, err
	}
	acts, err := s.searchActivity(ctx, q, qvec, projectID, actMax)
	if err != nil {
		return nil, nil, err
	}
	s.logSearch(ctx, auth.Actor(ctx), q, tiers.fts, tiers.semantic, tiers.trigram, len(acts), len(items))
	return items, acts, nil
}

// tierHits counts how many results each recall tier contributed to a search —
// the raw material for tuning search from observed behavior.
type tierHits struct {
	fts, semantic, trigram int
}

// semanticMaxDistance is the cosine-distance ceiling (0=identical, 2=opposite)
// for a vector hit to count as relevant. Beyond it semantic contributes nothing
// rather than surfacing the merely-least-distant row. Tunable via
// FLIGHTDECK_SEMANTIC_MAX_DISTANCE.
var semanticMaxDistance = envFloat("FLIGHTDECK_SEMANTIC_MAX_DISTANCE", 0.6)

func (s *Service) searchItems(ctx context.Context, q string, qvec *pgvec.Vector, projectID pgtype.UUID, typ *string, lim *int32, maxBody int) ([]dto.Item, tierHits, error) {
	var tiers tierHits
	fts, err := s.St.SearchItems(ctx, store.SearchItemsParams{Q: q, ProjectID: projectID, Type: typ, Lim: lim})
	if err != nil {
		return nil, tiers, err
	}
	tiers.fts = len(fts)
	var sem []dto.Item
	if qvec != nil {
		rows, err := s.St.SearchItemsSemantic(ctx, store.SearchItemsSemanticParams{
			QueryEmbedding: *qvec,
			ProjectID:      projectID,
			Type:           typ,
			MaxDistance:    semanticMaxDistance,
			Lim:            lim,
		})
		if err != nil {
			// Semantic is best-effort: a vector-search hiccup must not break search.
			log.Printf("semantic item search (continuing lexical-only): %v", err)
		} else {
			sem = dto.ToSemanticItemsTrunc(rows, maxBody)
			tiers.semantic = len(sem)
		}
	}
	merged := rrfMerge(func(it dto.Item) string { return it.ID }, dto.ToSearchItemsTrunc(fts, maxBody), sem)
	if len(merged) > 0 {
		return capLen(merged, lim), tiers, nil
	}
	fz, err := s.St.SearchItemsFuzzy(ctx, store.SearchItemsFuzzyParams{Q: q, ProjectID: projectID, Type: typ, Lim: lim})
	if err != nil {
		return nil, tiers, err
	}
	tiers.trigram = len(fz)
	return dto.ToFuzzyItemsTrunc(fz, maxBody), tiers, nil
}

func (s *Service) searchActivity(ctx context.Context, q string, qvec *pgvec.Vector, projectID pgtype.UUID, maxBody int) ([]dto.Activity, error) {
	fts, err := s.St.SearchActivity(ctx, store.SearchActivityParams{Q: q, ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	var sem []dto.Activity
	if qvec != nil {
		rows, err := s.St.SearchActivitySemantic(ctx, store.SearchActivitySemanticParams{
			QueryEmbedding: *qvec,
			ProjectID:      projectID,
			MaxDistance:    semanticMaxDistance,
		})
		if err != nil {
			log.Printf("semantic activity search (continuing lexical-only): %v", err)
		} else {
			sem = dto.ToSemanticActivitiesTrunc(rows, maxBody)
		}
	}
	return rrfMerge(func(a dto.Activity) string { return a.ID }, dto.ToSearchActivitiesTrunc(fts, maxBody), sem), nil
}

// rrfMerge blends ranked result lists with reciprocal-rank fusion: each row
// scores sum(1/(60+rank)) across the lists it appears in, deduplicated by key.
// A row ranked well by both lexical and semantic beats one ranked by either
// alone; 60 is the standard RRF damping constant.
func rrfMerge[T any](key func(T) string, lists ...[]T) []T {
	type scored struct {
		val   T
		score float64
		seen  int // insertion order, for a stable tie-break
	}
	byKey := make(map[string]*scored)
	var order []*scored
	for _, list := range lists {
		for rank, v := range list {
			k := key(v)
			sc, ok := byKey[k]
			if !ok {
				sc = &scored{val: v, seen: len(order)}
				byKey[k] = sc
				order = append(order, sc)
			}
			sc.score += 1.0 / float64(60+rank)
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].score > order[j].score })
	out := make([]T, len(order))
	for i, sc := range order {
		out[i] = sc.val
	}
	return out
}

// capLen trims a merged result list to the caller's limit (default 25), since
// fusing two capped lists can exceed it.
func capLen[T any](list []T, lim *int32) []T {
	max := 25
	if lim != nil && *lim > 0 {
		max = int(*lim)
	}
	if len(list) > max {
		return list[:max]
	}
	return list
}

// BulkCreateItems creates several items in one call (each via CreateItem, so
// activity + outbox + idempotency all apply). Returns the created items; on the
// first failure it returns what succeeded plus the error.
func (s *Service) BulkCreateItems(ctx context.Context, ps []store.CreateItemParams, actor string) ([]store.Item, error) {
	out := make([]store.Item, 0, len(ps))
	for _, p := range ps {
		item, err := s.CreateItem(ctx, p, actor)
		if err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, nil
}
