package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrInvalidProjectParent identifies a parent change that would corrupt the
// project tree (self-parent or cycle). Mapped to 400 by transports.
var ErrInvalidProjectParent = errors.New("invalid project parent")

// ValidateProjectParent checks that setting slug's parent to parent is legal:
// the parent must exist (pgx.ErrNoRows preserved for a 404) and must not be
// slug itself or any of slug's descendants (a cycle). An empty parent (clear)
// is always legal. A concurrent parent change can in principle race two valid
// checks into a cycle; ProjectDescendants uses UNION so reads still terminate,
// and the next validated change repairs the tree.
func (s *Service) ValidateProjectParent(ctx context.Context, slug, parent string) error {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return nil
	}
	if _, err := s.St.GetProjectBySlug(ctx, parent); err != nil {
		return err
	}
	// slug may not exist yet (create): descendants of a nonexistent root is
	// empty, so the walk below degrades to the self-parent check.
	if parent == slug {
		return fmt.Errorf("%w: project cannot be its own parent", ErrInvalidProjectParent)
	}
	descendants, err := s.St.ProjectDescendants(ctx, slug)
	if err != nil {
		return err
	}
	if slices.Contains(descendants, parent) {
		return fmt.Errorf("%w: %q is a descendant of %q — this would create a cycle", ErrInvalidProjectParent, parent, slug)
	}
	return nil
}
