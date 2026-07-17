package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"flightdeck/internal/dto"
	"flightdeck/internal/store"
)

// NextAction ranks open, unblocked items ("ready to work") across active
// projects, or within one project when slug is non-empty.
func (s *Service) NextAction(ctx context.Context, slug string, limit int32) (dto.NextAction, error) {
	var projectID pgtype.UUID
	if slug != "" {
		p, err := s.St.GetProjectBySlug(ctx, slug)
		if err != nil {
			return dto.NextAction{}, err
		}
		projectID = projectUUID(p.ID)
	}
	var lim *int32
	if limit > 0 {
		lim = &limit
	}
	rows, err := s.St.ListReadyItems(ctx, store.ListReadyItemsParams{ProjectID: projectID, Lim: lim})
	if err != nil {
		return dto.NextAction{}, err
	}
	out := dto.NextAction{Items: make([]dto.ItemWithProject, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, dto.ReadyRowToItem(r))
	}
	return out, nil
}

// Digest rolls up a project's activity since a timestamp into a compact bundle.
func (s *Service) Digest(ctx context.Context, slug string, since time.Time) (dto.Digest, error) {
	p, err := s.St.GetProjectBySlug(ctx, slug)
	if err != nil {
		return dto.Digest{}, err
	}
	counts, err := s.St.ActivityKindCountsSince(ctx, store.ActivityKindCountsSinceParams{ProjectID: p.ID, CreatedAt: since})
	if err != nil {
		return dto.Digest{}, err
	}
	touched, err := s.St.CountDistinctItemsTouchedSince(ctx, store.CountDistinctItemsTouchedSinceParams{ProjectID: p.ID, CreatedAt: since})
	if err != nil {
		return dto.Digest{}, err
	}
	decisionKind := "decision"
	limit := int32(50)
	decisions, err := s.St.ListActivity(ctx, store.ListActivityParams{
		ProjectID: projectUUID(p.ID),
		Kind:      &decisionKind,
		Since:     &since,
		Lim:       &limit,
	})
	if err != nil {
		return dto.Digest{}, err
	}
	cmap := make(map[string]int, len(counts))
	for _, c := range counts {
		cmap[c.Kind] = int(c.N)
	}
	return dto.Digest{
		Project:        slug,
		Since:          since,
		Counts:         cmap,
		ItemsTouched:   int(touched),
		Decisions:      dto.ToActivities(decisions),
		CurrentSummary: p.Summary,
	}, nil
}

// Stale surfaces in_progress items idle since inProgressBefore, bugs left in
// backlog since bugBefore, and projects whose summary predates their activity.
func (s *Service) Stale(ctx context.Context, inProgressBefore, bugBefore time.Time) (dto.StaleReport, error) {
	ip, err := s.St.ListStaleInProgress(ctx, inProgressBefore)
	if err != nil {
		return dto.StaleReport{}, err
	}
	bugs, err := s.St.ListUntriagedBugs(ctx, bugBefore)
	if err != nil {
		return dto.StaleReport{}, err
	}
	sums, err := s.St.ListStaleProjectSummaries(ctx)
	if err != nil {
		return dto.StaleReport{}, err
	}
	rep := dto.StaleReport{
		StaleInProgress: make([]dto.ItemWithProject, 0, len(ip)),
		UntriagedBugs:   make([]dto.ItemWithProject, 0, len(bugs)),
		StaleSummaries:  make([]dto.StaleSummary, 0, len(sums)),
	}
	for _, r := range ip {
		rep.StaleInProgress = append(rep.StaleInProgress, dto.StaleInProgressToItem(r))
	}
	for _, r := range bugs {
		rep.UntriagedBugs = append(rep.UntriagedBugs, dto.UntriagedBugToItem(r))
	}
	for _, r := range sums {
		rep.StaleSummaries = append(rep.StaleSummaries, dto.ToStaleSummary(r))
	}
	return rep, nil
}

// ResolveProject finds the project an agent is standing in from a filesystem
// path or git remote, matching path segments against each project's slug, name,
// aliases, and repo basename. This replaces the hand-kept cwd→slug table: the
// agent passes its cwd and gets the slug. The longest (most specific) key match
// wins so "/…/dev/personal-site" beats a stray "site" token.
func (s *Service) ResolveProject(ctx context.Context, hint string) (store.Project, error) {
	projects, err := s.St.ListProjects(ctx, nil)
	if err != nil {
		return store.Project{}, err
	}
	segs := pathSegments(hint)
	segSet := make(map[string]bool, len(segs))
	for _, seg := range segs {
		segSet[seg] = true
	}
	var best store.Project
	bestLen := 0
	for _, p := range projects {
		for _, key := range projectKeys(p) {
			if segSet[key] && len(key) > bestLen {
				best, bestLen = p, len(key)
			}
		}
	}
	if bestLen == 0 {
		slugs := make([]string, len(projects))
		for i, p := range projects {
			slugs[i] = p.Slug
		}
		return store.Project{}, fmt.Errorf("no project matches %q; known projects: %s", hint, strings.Join(slugs, ", "))
	}
	return best, nil
}

// pathSegments lowercases and splits a path / remote URL into comparable tokens,
// dropping a trailing ".git" and generic ancestors that never identify a repo.
func pathSegments(hint string) []string {
	hint = strings.TrimSpace(hint)
	hint = strings.TrimSuffix(hint, ".git")
	hint = strings.ReplaceAll(hint, "\\", "/")
	hint = strings.ReplaceAll(hint, ":", "/") // git@github.com:owner/repo
	var out []string
	for _, raw := range strings.Split(hint, "/") {
		seg := strings.ToLower(strings.TrimSpace(raw))
		switch seg {
		case "", "users", "home", "dev", "desktop", "documents", "src", "code", "repos", "github.com":
			continue
		}
		out = append(out, seg)
	}
	return out
}

// projectKeys is the set of lowercased identifiers a path token may match.
func projectKeys(p store.Project) []string {
	keys := []string{strings.ToLower(p.Slug), strings.ToLower(p.Name)}
	for _, a := range p.Aliases {
		keys = append(keys, strings.ToLower(a))
	}
	if p.RepoUrl != nil {
		base := strings.TrimSuffix(strings.ToLower(*p.RepoUrl), ".git")
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		if base != "" {
			keys = append(keys, base)
		}
	}
	return keys
}

// CompleteItem closes an item in one call: marks it done (which appends a
// status_change), logs a decision capturing the why, and optionally refreshes
// the project summary — matching the unit of work an agent actually finishes.
// Everything runs in ONE transaction: the tool's whole promise is that an item
// never ends up done without its recorded why, so a partial failure rolls the
// status change back too.
func (s *Service) CompleteItem(ctx context.Context, itemID uuid.UUID, why, summary string, actor string) (store.Item, error) {
	var updated store.Item
	err := s.St.WithTx(ctx, func(q *store.Queries) error {
		prev, err := q.GetItem(ctx, itemID)
		if err != nil {
			return err
		}
		done := "done"
		updated, err = q.UpdateItem(ctx, store.UpdateItemParams{ID: itemID, Status: &done})
		if err != nil {
			return err
		}
		if prev.Status != updated.Status {
			if _, err := q.CreateActivity(ctx, store.CreateActivityParams{
				ProjectID: updated.ProjectID,
				ItemID:    itemUUID(itemID),
				Kind:      strptr("status_change"),
				Actor:     strptr(actor),
				Body:      strptr(fmt.Sprintf("%s → %s", prev.Status, updated.Status)),
			}); err != nil {
				return err
			}
			if err := s.enqueue(ctx, q, updated.ProjectID, "item.status_changed", map[string]any{
				"item": dto.ToItem(updated), "from": prev.Status, "to": updated.Status,
			}); err != nil {
				return err
			}
		}
		if why != "" {
			row, err := q.CreateActivity(ctx, store.CreateActivityParams{
				ProjectID: updated.ProjectID,
				ItemID:    itemUUID(itemID),
				Kind:      strptr("decision"),
				Actor:     strptr(actor),
				Body:      strptr(why),
			})
			if err != nil {
				return err
			}
			if err := s.enqueue(ctx, q, updated.ProjectID, "activity.logged", dto.ToActivity(row)); err != nil {
				return err
			}
		}
		if summary != "" {
			p, err := q.GetProjectByID(ctx, updated.ProjectID)
			if err != nil {
				return err
			}
			if _, err := q.UpdateProjectSummary(ctx, store.UpdateProjectSummaryParams{Slug: p.Slug, Summary: summary}); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		s.cache.clear()
	}
	return updated, err
}

// LinkItems creates (or upserts) a directed link between two items.
func (s *Service) LinkItems(ctx context.Context, from, to uuid.UUID, kind string) (store.ItemLink, error) {
	var k *string
	if kind != "" {
		k = &kind
	}
	return s.St.CreateItemLink(ctx, store.CreateItemLinkParams{FromItemID: from, ToItemID: to, Kind: k})
}

// DeleteLink removes an item link by its id.
func (s *Service) DeleteLink(ctx context.Context, id uuid.UUID) error {
	return s.St.DeleteItemLink(ctx, id)
}

// ListLinks returns every link touching an item (incoming or outgoing).
func (s *Service) ListLinks(ctx context.Context, itemID uuid.UUID) ([]store.ItemLink, error) {
	return s.St.ListLinksForItem(ctx, itemID)
}

// AddItemRef grounds an item to a code coordinate (commit/file/pr/branch/url).
func (s *Service) AddItemRef(ctx context.Context, itemID uuid.UUID, kind, ref, label string) (store.ItemRef, error) {
	var k, l *string
	if kind != "" {
		k = &kind
	}
	if label != "" {
		l = &label
	}
	return s.St.CreateItemRef(ctx, store.CreateItemRefParams{ItemID: itemID, Kind: k, Ref: ref, Label: l})
}

// ListItemRefs returns an item's code references.
func (s *Service) ListItemRefs(ctx context.Context, itemID uuid.UUID) ([]store.ItemRef, error) {
	return s.St.ListItemRefs(ctx, itemID)
}

// DeleteItemRef removes a code reference by id.
func (s *Service) DeleteItemRef(ctx context.Context, id uuid.UUID) error {
	return s.St.DeleteItemRef(ctx, id)
}
