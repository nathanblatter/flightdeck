package dto

import (
	"time"

	"flightdeck/internal/store"
)

// AgendaEntry is one dated open item on the cross-project agenda.
type AgendaEntry struct {
	Ref      string    `json:"ref"`
	Project  string    `json:"project"`
	Title    string    `json:"title"`
	Status   string    `json:"status"`
	Priority string    `json:"priority"`
	Type     string    `json:"type"`
	DueAt    time.Time `json:"due_at"`
	// Overdue: due_at is in the past and the item is still open.
	Overdue bool `json:"overdue,omitempty"`
	// LastActivityAt is the newest activity on the item (its creation when
	// nothing else was ever logged).
	LastActivityAt time.Time `json:"last_activity_at"`
	// Expired: overdue with no activity since the due date — nobody has said
	// what happened. The item needs closing, re-dating, or a note.
	Expired bool `json:"expired,omitempty"`
}

// Agenda is the "what is due" view across active projects: overdue items
// first (oldest due first), then everything due inside the window.
type Agenda struct {
	Days     int           `json:"days"`
	Now      time.Time     `json:"now"`
	Overdue  []AgendaEntry `json:"overdue"`
	Upcoming []AgendaEntry `json:"upcoming"`
	// Nudges are the housekeeping lines (expired items) an agent should act on.
	Nudges []string `json:"nudges,omitempty"`
}

// AgendaRowToEntry converts a store row into an agenda entry, deriving the
// overdue / expired flags against now.
func AgendaRowToEntry(r store.ListAgendaItemsRow, now time.Time) AgendaEntry {
	e := AgendaEntry{
		Ref: r.Ref, Project: r.ProjectSlug, Title: r.Title, Status: r.Status,
		Priority: r.Priority, Type: r.Type, LastActivityAt: r.LastActivity,
	}
	if r.DueAt != nil {
		e.DueAt = *r.DueAt
		e.Overdue = e.DueAt.Before(now)
		e.Expired = e.Overdue && !r.LastActivity.After(e.DueAt)
	}
	return e
}
