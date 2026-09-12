package service

import (
	"context"
	"errors"
	"fmt"
	"log"

	"flightdeck/internal/store"
)

// ErrProjectNotEmpty identifies a purge refused because the project still has
// children or has not been archived first. Mapped to 409 by transports.
var ErrProjectNotEmpty = errors.New("project not purgeable")

// ProjectPurgeCounts reports what a purge destroyed (or would destroy).
type ProjectPurgeCounts struct {
	Items       int64 `json:"items"`
	Activity    int64 `json:"activity"`
	Attachments int64 `json:"attachments"`
}

// ArchiveProject is the reversible half of deletion: it flips status to
// 'archived', which hides the project from orient reads and the default UI
// while keeping every row. Nothing is destroyed, so agents are allowed to do
// this; PurgeProject is the irreversible half and is deliberately not exposed
// over MCP.
func (s *Service) ArchiveProject(ctx context.Context, slug string) error {
	archived := "archived"
	if _, err := s.St.UpdateProject(ctx, store.UpdateProjectParams{Slug: slug, Status: &archived}); err != nil {
		return err
	}
	s.cache.clear()
	return nil
}

// PurgeProject hard-deletes an archived project and everything under it. The
// two guards are the whole safety story, since this cannot be undone:
//
//   - the project must already be status='archived', so a purge is always a
//     second, deliberate act rather than one mistaken call, and
//   - it must have no child projects. The parent_slug FK would silently
//     re-root them (ON DELETE SET NULL), quietly restructuring the tree as a
//     side effect of a delete — better to make the caller move them first.
//
// Attachment blobs are removed before the rows cascade away; after the delete
// nothing points at them and the maintenance sweep can no longer find them.
func (s *Service) PurgeProject(ctx context.Context, slug string) (ProjectPurgeCounts, error) {
	var counts ProjectPurgeCounts
	p, err := s.St.GetProjectBySlug(ctx, slug)
	if err != nil {
		return counts, err
	}
	if p.Status != "archived" {
		return counts, fmt.Errorf("%w: %q is %s — archive it first (a purge cannot be undone)", ErrProjectNotEmpty, slug, p.Status)
	}
	children, err := s.St.ListChildProjects(ctx, &slug)
	if err != nil {
		return counts, err
	}
	if len(children) > 0 {
		return counts, fmt.Errorf("%w: %q still has %d child project(s) — re-parent or purge them first", ErrProjectNotEmpty, slug, len(children))
	}

	tally, err := s.St.CountProjectContents(ctx, p.ID)
	if err != nil {
		return counts, err
	}
	counts.Items, counts.Activity = tally.Items, tally.Activity

	// Collect blob keys while the rows still exist, but only delete the objects
	// once the DB delete has committed — a failed delete must not leave the
	// project alive with its attachment bytes already gone.
	var keys []string
	if s.blob != nil {
		if keys, err = s.St.ListAttachmentKeysForProject(ctx, p.ID); err != nil {
			return counts, err
		}
	}
	if _, err := s.St.DeleteProject(ctx, slug); err != nil {
		return counts, err
	}
	s.cache.clear()

	counts.Attachments = int64(len(keys))
	for _, k := range keys {
		// Best-effort: the project is already gone, so a blob failure is an
		// orphaned object to log, not a reason to report the purge as failed.
		if err := s.blob.Delete(ctx, k); err != nil {
			log.Printf("purge project %s: delete blob %s: %v", slug, k, err)
		}
	}
	return counts, nil
}
