package service

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"flightdeck/internal/dto"
	"flightdeck/internal/store"
)

// recurMaxChars caps a recur rule. Interval forms are a handful of characters;
// free-text rules are a sentence an agent reads on completion, not a body.
const recurMaxChars = 200

// Recur is a parsed recurrence rule. Two forms exist:
//
//   - interval: an ISO-8601 duration subset — P<n>D, P<n>W, P<n>M or P<n>Y
//     (e.g. P1Y, P6M, P10D). Completing the item spawns a successor due one
//     interval after the predecessor's due date.
//   - free text: anything else ("at 72,900 mi", "every even year in October").
//     The successor carries the rule with no due date and the completion
//     response nudges the agent to set one.
type Recur struct {
	Rule string
	// Interval is true for the P<n><unit> form; N and Unit are then set.
	Interval bool
	N        int
	Unit     byte // 'D', 'W', 'M' or 'Y'
}

// ParseRecur validates and classifies a recur rule. The rule is trimmed; an
// empty rule or one over recurMaxChars is rejected. Interval forms are
// recognised case-insensitively and must have a positive count.
func ParseRecur(s string) (Recur, error) {
	rule := strings.TrimSpace(s)
	if rule == "" {
		return Recur{}, fmt.Errorf("recur must not be empty")
	}
	if utf8.RuneCountInString(rule) > recurMaxChars {
		return Recur{}, fmt.Errorf("recur must be at most %d characters", recurMaxChars)
	}
	r := Recur{Rule: rule}
	up := strings.ToUpper(rule)
	if len(up) < 3 || up[0] != 'P' {
		return r, nil
	}
	unit := up[len(up)-1]
	if unit != 'D' && unit != 'W' && unit != 'M' && unit != 'Y' {
		return r, nil
	}
	digits := up[1 : len(up)-1]
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return r, nil // not an interval: free text that happens to start with P
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return Recur{}, fmt.Errorf("invalid recur interval %q (want P<n>D|W|M|Y with n > 0)", rule)
	}
	r.Rule = up // canonical form
	r.Interval, r.N, r.Unit = true, n, unit
	return r, nil
}

// Next returns base advanced by one interval. Calendar arithmetic (AddDate) so
// P1M from Jan 31 lands in early March the way Go normalises it; the agent can
// adjust, and the alternative (clamping) silently drifts the cadence. The math
// runs in UTC: pgx returns timestamptz in the server's local zone, and AddDate
// across a DST change there would move the instant by an hour.
func (r Recur) Next(base time.Time) time.Time {
	base = base.UTC()
	switch r.Unit {
	case 'D':
		return base.AddDate(0, 0, r.N)
	case 'W':
		return base.AddDate(0, 0, 7*r.N)
	case 'M':
		return base.AddDate(0, r.N, 0)
	case 'Y':
		return base.AddDate(r.N, 0, 0)
	}
	return base
}

// ValidateRecur checks an optional recur rule on a write. nil is skipped. An
// empty string is allowed on update (it clears the rule) — callers turn that
// into ClearRecur before reaching here, so a non-nil empty value is rejected.
func ValidateRecur(recur *string) error {
	if recur == nil {
		return nil
	}
	_, err := ParseRecur(*recur)
	return err
}

// RecurParam normalises a caller-supplied recur value into UpdateItem's
// three-way convention: absent keeps, empty string clears, a value replaces it
// (canonicalised, e.g. "p1y" → "P1Y"). It validates the value.
func RecurParam(recur *string) (value *string, clear bool, err error) {
	if recur == nil {
		return nil, false, nil
	}
	if strings.TrimSpace(*recur) == "" {
		return nil, true, nil
	}
	r, err := ParseRecur(*recur)
	if err != nil {
		return nil, false, err
	}
	return &r.Rule, false, nil
}

// CreateRecurParam is RecurParam for creates, where empty means "no rule".
func CreateRecurParam(recur *string) (*string, error) {
	v, _, err := RecurParam(recur)
	return v, err
}

// ItemWrite is the result of a write that may close an item: the item itself
// and, when closing it spawned a recurrence, the successor.
type ItemWrite struct {
	Item    store.Item
	Spawned *store.Item
}

// Nudges returns the caller-facing hints for this write: a free-text
// recurrence spawns a successor with no due date, and the agent that just
// completed the predecessor is the one who can set it.
func (w ItemWrite) Nudges() []string {
	if w.Spawned == nil || w.Spawned.Recur == nil || w.Spawned.DueAt != nil {
		return nil
	}
	return []string{fmt.Sprintf("recurrence spawned %s with no due date — set due_at from the rule: %s", w.Spawned.Ref, *w.Spawned.Recur)}
}

// DTO renders the write as the item with its spawned successor (as a brief)
// and nudges attached — the shape update_item / complete_item return.
func (w ItemWrite) DTO() dto.Item {
	out := dto.ToItem(w.Item)
	if w.Spawned != nil {
		b := dto.ToItemBrief(dto.ToItem(*w.Spawned))
		out.Spawned = &b
		out.Nudges = w.Nudges()
	}
	return out
}

// closedByWrite reports whether a write moved an item into done — the single
// transition that spawns a recurrence. A done→done re-update (or wontfix,
// which ends the chain) never spawns, so each completion spawns exactly once.
func closedByWrite(prev, next store.Item) bool {
	return prev.Status != "done" && next.Status == "done"
}

// spawnRecurrence creates the successor of a just-completed recurring item on
// the same transaction: same project/type/title/body/priority/tags/rule,
// status todo, source agent. Interval rules set due_at one interval after the
// predecessor's due date (or after now when it had none); free-text rules
// leave due_at unset for the agent to fill in. The predecessor is linked to the
// successor (relates_to) and the successor's log records where it came from.
// Returns nil when the item has no rule.
func (s *Service) spawnRecurrence(ctx context.Context, q *store.Queries, pred store.Item, actor string) (*store.Item, error) {
	if pred.Recur == nil {
		return nil, nil
	}
	r, err := ParseRecur(*pred.Recur)
	if err != nil {
		// A rule that fails validation (only reachable by writing the column
		// directly) must not block the completion; it just ends the chain.
		log.Printf("recur: item %s has unparseable rule %q: %v", pred.Ref, *pred.Recur, err)
		return nil, nil
	}
	var due *time.Time
	if r.Interval {
		base := time.Now()
		if pred.DueAt != nil {
			base = *pred.DueAt
		}
		next := r.Next(base)
		due = &next
	}
	succ, err := s.createItemTx(ctx, q, store.CreateItemParams{
		ProjectID: pred.ProjectID,
		Type:      strptr(pred.Type),
		Title:     pred.Title,
		Body:      strptr(pred.Body),
		Status:    strptr("todo"),
		Priority:  strptr(pred.Priority),
		Source:    strptr("agent"),
		Tags:      pred.Tags,
		DueAt:     due,
		Recur:     strptr(r.Rule),
	}, actor)
	if err != nil {
		return nil, err
	}
	if _, err := q.CreateItemLink(ctx, store.CreateItemLinkParams{
		FromItemID: pred.ID, ToItemID: succ.ID, Kind: strptr("relates_to"),
	}); err != nil {
		return nil, err
	}
	if _, err := q.CreateActivity(ctx, store.CreateActivityParams{
		ProjectID: succ.ProjectID,
		ItemID:    itemUUID(succ.ID),
		Kind:      strptr("comment"),
		Actor:     strptr(actor),
		Body:      strptr(fmt.Sprintf("recurrence of %s", pred.Ref)),
	}); err != nil {
		return nil, err
	}
	return &succ, nil
}
