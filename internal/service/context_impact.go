package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"flightdeck/internal/store"
)

// ErrInvalidContextImpact identifies caller-controlled validation failures.
var ErrInvalidContextImpact = errors.New("invalid context impact")

// ContextImpactInput is one agent-reported effect of retrieved context during
// a caller-defined work session.
type ContextImpactInput struct {
	SessionID             string
	Project               string
	Item                  *string
	Effect                string
	Mechanism             string
	ContextRefs           []string
	Evidence              string
	EstimatedMinutesDelta *int32
	IdempotencyKey        *string
}

// RecordContextImpact validates and persists one immutable context outcome.
// The bool is false when an idempotent retry returns a prior event.
func (s *Service) RecordContextImpact(ctx context.Context, input ContextImpactInput, actor string) (store.ContextImpactEvent, bool, error) {
	input = normalizeContextImpact(input)
	if err := ValidateContextImpact(input); err != nil {
		return store.ContextImpactEvent{}, false, fmt.Errorf("%w: %v", ErrInvalidContextImpact, err)
	}

	project, err := s.St.GetProjectBySlug(ctx, input.Project)
	if err != nil {
		return store.ContextImpactEvent{}, false, err
	}

	var itemRef *string
	if input.Item != nil {
		item, err := s.contextImpactItem(ctx, *input.Item)
		if err != nil {
			return store.ContextImpactEvent{}, false, err
		}
		if item.ProjectID != project.ID {
			return store.ContextImpactEvent{}, false, fmt.Errorf(
				"%w: item %q does not belong to project %q",
				ErrInvalidContextImpact, *input.Item, input.Project,
			)
		}
		itemRef = &item.Ref
	}

	if actor = strings.TrimSpace(actor); actor == "" {
		actor = "unknown"
	}
	if input.IdempotencyKey != nil {
		prior, err := s.St.GetContextImpactByIdempotencyKey(ctx, store.GetContextImpactByIdempotencyKeyParams{
			Actor: actor, IdempotencyKey: input.IdempotencyKey,
		})
		if err == nil {
			return prior, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return store.ContextImpactEvent{}, false, err
		}
	}

	params := store.CreateContextImpactEventParams{
		Actor: actor, SessionID: input.SessionID, Project: input.Project,
		Item: itemRef, Effect: input.Effect, Mechanism: input.Mechanism,
		ContextRefs: input.ContextRefs, Evidence: input.Evidence,
		EstimatedMinutesDelta: input.EstimatedMinutesDelta,
		IdempotencyKey:        input.IdempotencyKey,
	}
	event, err := s.St.CreateContextImpactEvent(ctx, params)
	if err == nil {
		return event, true, nil
	}
	if input.IdempotencyKey != nil && isUniqueViolation(err) {
		prior, getErr := s.St.GetContextImpactByIdempotencyKey(ctx, store.GetContextImpactByIdempotencyKeyParams{
			Actor: actor, IdempotencyKey: input.IdempotencyKey,
		})
		if getErr == nil {
			return prior, false, nil
		}
	}
	return store.ContextImpactEvent{}, false, err
}

func (s *Service) contextImpactItem(ctx context.Context, ref string) (store.Item, error) {
	if id, err := uuid.Parse(ref); err == nil {
		return s.St.GetItem(ctx, id)
	}
	return s.St.GetItemByRef(ctx, ref)
}

func normalizeContextImpact(input ContextImpactInput) ContextImpactInput {
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.Project = strings.TrimSpace(input.Project)
	input.Effect = strings.TrimSpace(input.Effect)
	input.Mechanism = strings.TrimSpace(input.Mechanism)
	input.Evidence = strings.TrimSpace(input.Evidence)
	if input.Item != nil {
		item := strings.TrimSpace(*input.Item)
		if item == "" {
			input.Item = nil
		} else {
			input.Item = &item
		}
	}
	refs := make([]string, 0, len(input.ContextRefs))
	for _, ref := range input.ContextRefs {
		refs = append(refs, strings.TrimSpace(ref))
	}
	input.ContextRefs = refs
	if input.IdempotencyKey != nil {
		key := strings.TrimSpace(*input.IdempotencyKey)
		if key == "" {
			input.IdempotencyKey = nil
		} else {
			input.IdempotencyKey = &key
		}
	}
	return input
}
