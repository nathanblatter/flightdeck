package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"flightdeck/internal/auth"
	"flightdeck/internal/store"
)

// Project-scoped API keys (migration 00024). Keys store project ids; humans
// type slugs. These helpers sit between the two and are shared by the CLI
// (`flightdeck keygen --projects`, `flightdeck keys restrict`) and the tests.
// They take the store directly because the CLI runs without a Service.

// ResolveKeyProjects turns project references (slugs, or aliases such as an
// old slug kept after a rename) into project ids. An exact slug wins; failing
// that, a case-insensitive alias match is accepted when it is unambiguous.
// Every reference must resolve, and the result is deduplicated in input
// order. An empty list is an error: restricting a key to nothing is what
// revoke is for.
func ResolveKeyProjects(ctx context.Context, st *store.Store, refs []string) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	seen := map[uuid.UUID]bool{}
	for _, raw := range refs {
		ref := strings.TrimSpace(raw)
		if ref == "" {
			continue
		}
		p, err := resolveProjectRef(ctx, st, ref)
		if err != nil {
			return nil, err
		}
		if !seen[p.ID] {
			seen[p.ID] = true
			ids = append(ids, p.ID)
		}
	}
	if len(ids) == 0 {
		return nil, errors.New("at least one project is required for a restriction")
	}
	return ids, nil
}

func resolveProjectRef(ctx context.Context, st *store.Store, ref string) (store.Project, error) {
	p, err := st.GetProjectBySlug(ctx, ref)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return store.Project{}, err
	}
	matches, err := st.FindProjectsByAlias(ctx, ref)
	if err != nil {
		return store.Project{}, err
	}
	switch len(matches) {
	case 0:
		return store.Project{}, fmt.Errorf("no project with slug or alias %q", ref)
	case 1:
		return matches[0], nil
	default:
		slugs := make([]string, len(matches))
		for i, m := range matches {
			slugs[i] = m.Slug
		}
		return store.Project{}, fmt.Errorf("alias %q is ambiguous (matches %s); use the slug", ref, strings.Join(slugs, ", "))
	}
}

// RestrictAPIKey limits an existing key to the referenced projects without
// changing its secret. The key must hold only the ingest scope (see
// auth.ErrRestrictedScopes); the database enforces the same rule.
func RestrictAPIKey(ctx context.Context, st *store.Store, keyID uuid.UUID, refs []string) (store.SetAPIKeyProjectsRow, error) {
	key, err := st.GetAPIKeyByID(ctx, keyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.SetAPIKeyProjectsRow{}, fmt.Errorf("no key with id %s", keyID)
		}
		return store.SetAPIKeyProjectsRow{}, err
	}
	if err := auth.CheckRestrictionScopes(key.Scopes); err != nil {
		return store.SetAPIKeyProjectsRow{}, fmt.Errorf("key %q has scopes %v: %w", key.Name, key.Scopes, err)
	}
	ids, err := ResolveKeyProjects(ctx, st, refs)
	if err != nil {
		return store.SetAPIKeyProjectsRow{}, err
	}
	return st.SetAPIKeyProjects(ctx, store.SetAPIKeyProjectsParams{ID: keyID, ProjectIds: ids})
}

// UnrestrictAPIKey removes a key's project restriction (NULL = all projects).
func UnrestrictAPIKey(ctx context.Context, st *store.Store, keyID uuid.UUID) (store.SetAPIKeyProjectsRow, error) {
	row, err := st.SetAPIKeyProjects(ctx, store.SetAPIKeyProjectsParams{ID: keyID, ProjectIds: nil})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, fmt.Errorf("no key with id %s", keyID)
	}
	return row, err
}

// DescribeKeyProjects renders a key's restriction for listings: "all" when
// unrestricted, otherwise the current slugs, with "deleted:<id>" for a project
// that no longer exists (such an entry matches nothing — the key fails closed
// for it). Slugs are looked up live, so a renamed project shows its new slug.
func DescribeKeyProjects(ctx context.Context, st *store.Store, ids []uuid.UUID) (string, error) {
	if ids == nil {
		return "all", nil
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		p, err := st.GetProjectByID(ctx, id)
		switch {
		case err == nil:
			parts = append(parts, p.Slug)
		case errors.Is(err, pgx.ErrNoRows):
			parts = append(parts, "deleted:"+id.String())
		default:
			return "", err
		}
	}
	return strings.Join(parts, ","), nil
}
