package service

import (
	"fmt"
	"strings"
)

// Valid enum values, mirroring the CHECK constraints in migrations/00001_init.sql.
// Validating at the app layer turns bad input into a 400 with a helpful message
// instead of a 500 from the database CHECK constraint.
var (
	ValidItemTypes      = []string{"task", "bug", "idea", "note"}
	ValidItemStatuses   = []string{"backlog", "todo", "in_progress", "blocked", "done", "wontfix"}
	ValidItemPriorities = []string{"low", "med", "high", "urgent"}
	ValidActivityKinds  = []string{"decision", "progress", "status_change", "comment", "created", "rejected"}
	ValidLinkKinds      = []string{"blocks", "relates_to", "parent_of"}
	ValidConfidences    = []string{"unspecified", "inferred", "confirmed"}
	ValidRefKinds       = []string{"commit", "file", "pr", "branch", "url"}
)

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
