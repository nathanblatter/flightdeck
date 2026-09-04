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
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"flightdeck/internal/auth"
	"flightdeck/internal/blob"
	"flightdeck/internal/dto"
	"flightdeck/internal/embed"
	"flightdeck/internal/metrics"
	"flightdeck/internal/pgvec"
	"flightdeck/internal/store"
)

type Service struct {
	St     *store.Store
	hc     *http.Client
	cache  *ttlCache
	emb    *embed.Client
	vcache *vecCache
	sf     singleflight.Group // collapses concurrent identical orient reads
	hub    *Hub               // in-process pub/sub for live UI (SSE)
	blob   blob.Store         // object storage for attachments; nil = disabled

	// settings is the atomic snapshot of the settings table (instance name,
	// feature flags); see ReloadSettings in settings.go.
	settings atomic.Pointer[instanceSettings]
}

func New(st *store.Store) *Service {
	emb := embed.NewFromEnv()
	s := &Service{
		St:     st,
		hc:     &http.Client{Timeout: 10 * time.Second},
		cache:  newTTLCache(3 * time.Second),
		emb:    emb,
		vcache: newVecCache(emb.Model()),
		hub:    NewHub(),
	}
	s.ReloadSettings(context.Background())
	return s
}

// Hub exposes the live-event bus so the HTTP layer can serve SSE subscribers.
func (s *Service) Hub() *Hub { return s.hub }

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
		item, err = s.createItemTx(ctx, q, p, actor)
		return err
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

// createItemTx inserts one item plus its `created` activity and outbox event on
// an existing transaction's queryset. Kept separate from CreateItem so a batch
// can run many creates in a single tx (see BulkCreateItems). Honors an
// idempotency key by returning a prior item unchanged; the check runs inside the
// tx so it also sees items created earlier in the same batch.
func (s *Service) createItemTx(ctx context.Context, q *store.Queries, p store.CreateItemParams, actor string) (store.Item, error) {
	if p.IdempotencyKey != nil && *p.IdempotencyKey != "" {
		existing, err := q.GetItemByIdempotencyKey(ctx, store.GetItemByIdempotencyKeyParams{
			ProjectID: p.ProjectID, IdempotencyKey: p.IdempotencyKey,
		})
		if err == nil {
			return existing, nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return store.Item{}, err
		}
	}
	item, err := q.CreateItem(ctx, p)
	if err != nil {
		return store.Item{}, err
	}
	_, err = q.CreateActivity(ctx, store.CreateActivityParams{
		ProjectID: item.ProjectID,
		ItemID:    itemUUID(item.ID),
		Kind:      strptr("created"),
		Actor:     strptr(actor),
		Body:      strptr(fmt.Sprintf("created %s: %s", item.Type, item.Title)),
	})
	if err != nil {
		return store.Item{}, err
	}
	if err := s.enqueue(ctx, q, item.ProjectID, "item.created", dto.ToItem(item)); err != nil {
		return store.Item{}, err
	}
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
	// Collapse concurrent identical orients (common when several agents start on
	// the same project at once) into one build; the rest share the result.
	res, err, _ := s.sf.Do(key, func() (any, error) {
		if cached, ok := s.cache.get(key); ok { // won a race while queued
			return cached.(dto.ProjectContext), nil
		}
		return s.buildProjectContext(ctx, slug, v, key)
	})
	if err != nil {
		return dto.ProjectContext{}, err
	}
	return res.(dto.ProjectContext), nil
}

func (s *Service) buildProjectContext(ctx context.Context, slug string, v Verbosity, key string) (dto.ProjectContext, error) {
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
		children     []store.ListChildProjectsRow
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
	g.Go(func() (err error) {
		children, err = s.St.ListChildProjects(gctx, &p.Slug)
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
	if len(children) > 0 {
		bundle.Children = dto.ToProjectBriefs(children)
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
	res, err, _ := s.sf.Do(key, func() (any, error) {
		if cached, ok := s.cache.get(key); ok {
			return cached.(dto.GlobalContext), nil
		}
		return s.buildGlobalContext(ctx, v, key)
	})
	if err != nil {
		return dto.GlobalContext{}, err
	}
	return res.(dto.GlobalContext), nil
}

func (s *Service) buildGlobalContext(ctx context.Context, v Verbosity, key string) (dto.GlobalContext, error) {
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
	topByProject := make(map[uuid.UUID][]dto.ItemBrief, len(projects))
	for _, r := range rows {
		topByProject[r.ProjectID] = append(topByProject[r.ProjectID], dto.ToItemBrief(dto.TopOpenRowToItem(r, 0)))
	}
	out := dto.GlobalContext{Projects: make([]dto.ProjectOverview, 0, len(projects))}
	for _, p := range projects {
		proj := dto.ToProject(p)
		if v == VerbosityCompact {
			// Instructions are working-in-a-project material, read on the per-project
			// orient; repeating every project's here dominated the global payload.
			proj.Instructions = ""
		}
		out.Projects = append(out.Projects, dto.ProjectOverview{
			Project:  proj,
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
// minSemanticQueryLen is the shortest query (in runes, after trimming) worth
// embedding. Below it a query is a keystroke fragment ("j", "jo") that can't
// carry meaning — embedding it just burns the ~1.4s OpenAI round-trip on a
// zero-result path. Lexical (FTS/trigram) still runs. Tunable via
// FLIGHTDECK_MIN_SEMANTIC_QUERY_LEN.
var minSemanticQueryLen = int(envFloat("FLIGHTDECK_MIN_SEMANTIC_QUERY_LEN", 3))

// lazySemanticMinFTS is the lexical-recall bar above which semantic search is
// skipped entirely: if plain FTS already returned this many items, the query is
// well-served lexically and the embedding (and its latency) is pure waste. Only
// thin lexical recall pays for semantic. Tunable via
// FLIGHTDECK_LAZY_SEMANTIC_MIN_FTS.
var lazySemanticMinFTS = int(envFloat("FLIGHTDECK_LAZY_SEMANTIC_MIN_FTS", 5))

// embedTimeout bounds the live query-embedding call so a slow OpenAI response
// can't hold a search request open; on timeout search returns lexical results.
// Tunable via FLIGHTDECK_EMBED_TIMEOUT_MS.
var embedTimeout = time.Duration(envFloat("FLIGHTDECK_EMBED_TIMEOUT_MS", 800)) * time.Millisecond

func (s *Service) SearchSmart(ctx context.Context, q string, projectID pgtype.UUID, typ *string, lim *int32, itemMax, actMax int) ([]dto.Item, []dto.Activity, error) {
	// Record which recall path set this query's latency, so the win from lazy
	// semantic + the Redis cache is measurable rather than assumed.
	start := time.Now()
	path := "lexical"
	defer func() { metrics.Search(path, time.Since(start)) }()

	// Phase 1: lexical (FTS) on both surfaces, concurrently. These are ~10ms
	// Postgres reads and gate whether the expensive embed is worth doing.
	var itemFTS []store.SearchItemsRow
	var actFTS []store.SearchActivityRow
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		r, err := s.St.SearchItems(gctx, store.SearchItemsParams{Q: q, ProjectID: projectID, Type: typ, Lim: lim})
		itemFTS = r
		return err
	})
	g.Go(func() error {
		r, err := s.St.SearchActivity(gctx, store.SearchActivityParams{Q: q, ProjectID: projectID})
		actFTS = r
		return err
	})
	if err := g.Wait(); err != nil {
		return nil, nil, err
	}

	// Phase 2: embed only when semantic recall can actually help — the query is
	// long enough to mean something and lexical came up thin. Shared by both
	// semantic tiers, and served from the Redis cache when we've seen it before.
	var qvec *pgvec.Vector
	if s.wantSemantic(q, len(itemFTS)) {
		var cached bool
		qvec, cached = s.embedQuery(ctx, q)
		if cached {
			path = "cache"
		} else {
			path = "embed"
		}
	}

	// Phase 3: finalize both surfaces concurrently (semantic query + RRF merge,
	// plus the trigram fallback for items when lexical+semantic both whiff).
	var (
		items []dto.Item
		tiers tierHits
		acts  []dto.Activity
	)
	g2, g2ctx := errgroup.WithContext(ctx)
	g2.Go(func() error {
		var err error
		items, tiers, err = s.finalizeItems(g2ctx, q, itemFTS, qvec, projectID, typ, lim, itemMax)
		return err
	})
	g2.Go(func() error {
		var err error
		acts, err = s.finalizeActivity(g2ctx, actFTS, qvec, projectID, actMax)
		return err
	})
	if err := g2.Wait(); err != nil {
		return nil, nil, err
	}
	s.logSearch(ctx, auth.Actor(ctx), q, tiers.fts, tiers.semantic, tiers.trigram, len(acts), len(items))
	return items, acts, nil
}

// wantSemantic decides whether a query earns the embedding round-trip: it must
// be embeddable at all, longer than a keystroke fragment, and not already
// well-answered by lexical search.
func (s *Service) wantSemantic(q string, ftsHits int) bool {
	if !s.emb.Enabled() {
		return false
	}
	if utf8.RuneCountInString(strings.TrimSpace(q)) < minSemanticQueryLen {
		return false
	}
	return ftsHits < lazySemanticMinFTS
}

// embedQuery returns the query vector and whether it was served from the Redis
// cache (a hit skips the ~1.4s OpenAI round-trip). Best-effort throughout: any
// failure yields a nil vector and the caller falls back to lexical-only search.
// The cached flag reflects a real cache hit only — a live embed (or a failure)
// returns false.
func (s *Service) embedQuery(ctx context.Context, q string) (*pgvec.Vector, bool) {
	if vec, ok := s.vcache.get(ctx, q); ok {
		v := pgvec.New(vec)
		return &v, true
	}
	// Bound the live embed: a slow OpenAI call must not hold the request open.
	// On timeout the caller keeps the lexical results already in hand.
	ectx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()
	vecs, err := s.emb.Embed(ectx, []string{q})
	if err != nil {
		log.Printf("search: embedding query failed (lexical-only for this query): %v", err)
		return nil, false
	}
	if len(vecs) != 1 {
		return nil, false
	}
	s.vcache.set(ctx, q, vecs[0])
	v := pgvec.New(vecs[0])
	return &v, false
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

// finalizeItems augments the already-fetched item FTS rows with semantic recall
// (when a query vector is present) via RRF, falling back to trigram fuzzy match
// only when lexical and semantic both return nothing.
func (s *Service) finalizeItems(ctx context.Context, q string, fts []store.SearchItemsRow, qvec *pgvec.Vector, projectID pgtype.UUID, typ *string, lim *int32, maxBody int) ([]dto.Item, tierHits, error) {
	var tiers tierHits
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

// finalizeActivity augments the already-fetched activity FTS rows with semantic
// recall (when a query vector is present) via RRF.
func (s *Service) finalizeActivity(ctx context.Context, fts []store.SearchActivityRow, qvec *pgvec.Vector, projectID pgtype.UUID, maxBody int) ([]dto.Activity, error) {
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

// BulkCreateItems creates several items in a SINGLE transaction (each with its
// activity + outbox event + idempotency check). Atomic: if any item fails the
// whole batch rolls back and no items are created, rather than leaving a partial
// commit. One round trip instead of N.
func (s *Service) BulkCreateItems(ctx context.Context, ps []store.CreateItemParams, actor string) ([]store.Item, error) {
	out := make([]store.Item, 0, len(ps))
	err := s.St.WithTx(ctx, func(q *store.Queries) error {
		out = out[:0] // reset in case the tx body ever re-runs
		for _, p := range ps {
			item, err := s.createItemTx(ctx, q, p, actor)
			if err != nil {
				return err
			}
			out = append(out, item)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.cache.clear()
	return out, nil
}
