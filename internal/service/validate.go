package service

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Valid enum values, mirroring the CHECK constraints in migrations/00001_init.sql.
// Validating at the app layer turns bad input into a 400 with a helpful message
// instead of a 500 from the database CHECK constraint.
var (
	ValidItemTypes         = []string{"task", "bug", "idea", "note"}
	ValidItemStatuses      = []string{"backlog", "todo", "in_progress", "blocked", "done", "wontfix"}
	ValidItemPriorities    = []string{"low", "med", "high", "urgent"}
	ValidActivityKinds     = []string{"decision", "progress", "status_change", "comment", "created", "rejected"}
	ValidLinkKinds         = []string{"blocks", "relates_to", "parent_of"}
	ValidConfidences       = []string{"unspecified", "inferred", "confirmed"}
	ValidRefKinds          = []string{"commit", "file", "pr", "branch", "url"}
	ValidProjectStatus     = []string{"active", "paused", "done", "archived"}
	ValidContextEffects    = []string{"helpful", "neutral", "harmful"}
	ValidContextMechanisms = []string{
		"decision_changed", "prevented_error", "duplicate_work_avoided",
		"reconstruction_saved", "ignored", "stale_or_incorrect",
	}
)

var validContextImpactPairs = map[string]map[string]bool{
	"helpful": {
		"decision_changed":       true,
		"prevented_error":        true,
		"duplicate_work_avoided": true,
		"reconstruction_saved":   true,
	},
	"neutral": {"ignored": true},
	"harmful": {"stale_or_incorrect": true},
}

func oneOf(field, val string, allowed []string) error {
	for _, a := range allowed {
		if val == a {
			return nil
		}
	}
	return fmt.Errorf("invalid %s %q (want one of: %s)", field, val, strings.Join(allowed, ", "))
}

// ValidateItemFields checks the optional type/status/priority fields of an item
// write. nil or empty values are skipped (they fall back to defaults / no-op).
func ValidateItemFields(typ, status, priority *string) error {
	if typ != nil && *typ != "" {
		if err := oneOf("type", *typ, ValidItemTypes); err != nil {
			return err
		}
	}
	if status != nil && *status != "" {
		if err := oneOf("status", *status, ValidItemStatuses); err != nil {
			return err
		}
	}
	if priority != nil && *priority != "" {
		if err := oneOf("priority", *priority, ValidItemPriorities); err != nil {
			return err
		}
	}
	return nil
}

// ValidateProjectStatus checks an optional project status; nil/empty is skipped.
func ValidateProjectStatus(status *string) error {
	if status != nil && *status != "" {
		return oneOf("status", *status, ValidProjectStatus)
	}
	return nil
}

// ValidateContextImpact checks the bounded, internally consistent reporting
// contract before an impact event reaches PostgreSQL.
func ValidateContextImpact(in ContextImpactInput) error {
	sessionID := strings.TrimSpace(in.SessionID)
	if utf8.RuneCountInString(sessionID) < 1 || utf8.RuneCountInString(sessionID) > 200 {
		return fmt.Errorf("session_id must be 1-200 characters")
	}
	if strings.TrimSpace(in.Project) == "" {
		return fmt.Errorf("project is required")
	}
	if err := oneOf("effect", in.Effect, ValidContextEffects); err != nil {
		return err
	}
	if err := oneOf("mechanism", in.Mechanism, ValidContextMechanisms); err != nil {
		return err
	}
	if !validContextImpactPairs[in.Effect][in.Mechanism] {
		return fmt.Errorf("mechanism %q is not valid for effect %q", in.Mechanism, in.Effect)
	}
	evidence := strings.TrimSpace(in.Evidence)
	if utf8.RuneCountInString(evidence) < 1 || utf8.RuneCountInString(evidence) > 2000 {
		return fmt.Errorf("evidence must be 1-2000 characters")
	}
	if len(in.ContextRefs) > 20 {
		return fmt.Errorf("context_refs may contain at most 20 entries")
	}
	for _, ref := range in.ContextRefs {
		n := utf8.RuneCountInString(strings.TrimSpace(ref))
		if n < 1 || n > 200 {
			return fmt.Errorf("each context_refs entry must be 1-200 characters")
		}
	}
	if in.IdempotencyKey != nil && utf8.RuneCountInString(strings.TrimSpace(*in.IdempotencyKey)) > 200 {
		return fmt.Errorf("idempotency_key must be at most 200 characters")
	}
	if in.EstimatedMinutesDelta == nil {
		return nil
	}
	delta := *in.EstimatedMinutesDelta
	if delta < -1440 || delta > 1440 {
		return fmt.Errorf("estimated_minutes_delta must be between -1440 and 1440")
	}
	if in.Effect == "helpful" && delta < 0 {
		return fmt.Errorf("helpful impact cannot report negative estimated_minutes_delta")
	}
	if in.Effect == "harmful" && delta > 0 {
		return fmt.Errorf("harmful impact cannot report positive estimated_minutes_delta")
	}
	if in.Effect == "neutral" && delta != 0 {
		return fmt.Errorf("neutral impact must report zero estimated_minutes_delta")
	}
	return nil
}

// ValidateActivityKind checks an optional activity kind; nil/empty is skipped.
func ValidateActivityKind(kind *string) error {
	if kind != nil && *kind != "" {
		return oneOf("kind", *kind, ValidActivityKinds)
	}
	return nil
}

// ValidateLinkKind checks an optional item-link kind; nil/empty is skipped.
func ValidateLinkKind(kind *string) error {
	if kind != nil && *kind != "" {
		return oneOf("kind", *kind, ValidLinkKinds)
	}
	return nil
}

// ValidateConfidence checks an optional activity confidence; nil/empty is skipped.
func ValidateConfidence(confidence *string) error {
	if confidence != nil && *confidence != "" {
		return oneOf("confidence", *confidence, ValidConfidences)
	}
	return nil
}

// ValidateRefKind checks an optional item-ref kind; nil/empty is skipped.
func ValidateRefKind(kind *string) error {
	if kind != nil && *kind != "" {
		return oneOf("kind", *kind, ValidRefKinds)
	}
	return nil
}
