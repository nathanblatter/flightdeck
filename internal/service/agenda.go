package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"flightdeck/internal/dto"
	"flightdeck/internal/store"
)

// Agenda window bounds. Beyond agendaMaxDays an agenda is a backlog listing,
// not a calendar.
const (
	agendaDefaultDays = 7
	agendaMaxDays     = 90
	agendaLimit       = 200
)

// Agenda builds the cross-project "what is due" view: dated open items on
// active projects due on or before now+days, overdue first. Expired entries
// (overdue with nothing logged since the due date) are also surfaced as a
// nudge so an agent closes, re-dates, or explains them instead of letting them
// sit in todo.
func (s *Service) Agenda(ctx context.Context, days int) (dto.Agenda, error) {
	if days <= 0 {
		days = agendaDefaultDays
	}
	if days > agendaMaxDays {
		days = agendaMaxDays
	}
	now := time.Now()
	rows, err := s.St.ListAgendaItems(ctx, store.ListAgendaItemsParams{Horizon: now.Add(time.Duration(days) * 24 * time.Hour), Lim: agendaLimit})
	if err != nil {
		return dto.Agenda{}, err
	}
	out := dto.Agenda{Days: days, Now: now, Overdue: []dto.AgendaEntry{}, Upcoming: []dto.AgendaEntry{}}
	var expired []string
	for _, r := range rows {
		e := dto.AgendaRowToEntry(r, now)
		if e.Overdue {
			out.Overdue = append(out.Overdue, e)
			if e.Expired {
				expired = append(expired, e.Ref)
			}
		} else {
			out.Upcoming = append(out.Upcoming, e)
		}
	}
	if n := len(expired); n > 0 {
		out.Nudges = append(out.Nudges, fmt.Sprintf(
			"%s past due with no activity since the due date (%s) — expired? close, re-date, or log what happened",
			plural(n, "item"), strings.Join(expired, ", ")))
	}
	return out, nil
}

// dueNudges derives the overdue housekeeping lines for one project's open
// items (already loaded by orient). An overdue item nobody has touched since
// its due date is flagged as possibly expired; one that has been updated since
// is merely listed as overdue. updated_at is the signal here: activity rows
// are not loaded per item on orient, and any status/body/date edit bumps it.
func dueNudges(openItems []dto.Item, now time.Time) []string {
	var expired, overdue []string
	for _, it := range openItems {
		if it.DueAt == nil || !it.DueAt.Before(now) {
			continue
		}
		if it.UpdatedAt.Before(*it.DueAt) {
			expired = append(expired, it.Ref)
		} else {
			overdue = append(overdue, it.Ref)
		}
	}
	var nudges []string
	if n := len(expired); n > 0 {
		nudges = append(nudges, fmt.Sprintf(
			"%s past due with no update since the due date (%s) — expired? close, re-date, or log what happened",
			plural(n, "item"), strings.Join(expired, ", ")))
	}
	if n := len(overdue); n > 0 {
		nudges = append(nudges, fmt.Sprintf("%s overdue (%s)", plural(n, "item"), strings.Join(overdue, ", ")))
	}
	return nudges
}

// dueLocation is the timezone a date-only due value is read in: FLIGHTDECK_TZ,
// else TZ, else the process local zone. Resolved once; an instance has one
// owner and one calendar.
var dueLocation = func() *time.Location {
	for _, k := range []string{"FLIGHTDECK_TZ", "TZ"} {
		if v := os.Getenv(k); v != "" {
			if loc, err := time.LoadLocation(v); err == nil {
				return loc
			}
		}
	}
	return time.Local
}()

// ParseDue reads a due date as an agent or form supplies it: RFC3339, or a
// bare YYYY-MM-DD. A date-only value means the end of that day (23:59:59) in
// the instance timezone — "due Nov 28" is not overdue at 00:01 on the 28th.
// An empty string yields nil (no date).
func ParseDue(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t, nil
	}
	d, err := time.ParseInLocation("2006-01-02", s, dueLocation)
	if err != nil {
		return nil, fmt.Errorf("invalid due_at %q (want RFC3339 or YYYY-MM-DD)", s)
	}
	t := d.Add(24*time.Hour - time.Second)
	return &t, nil
}
