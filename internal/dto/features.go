package dto

import (
	"time"

	"github.com/google/uuid"

	"flightdeck/internal/store"
)

// ItemLink is a directed relationship between two items.
type ItemLink struct {
	ID         string    `json:"id"`
	FromItemID string    `json:"from_item_id"`
	ToItemID   string    `json:"to_item_id"`
	Kind       string    `json:"kind"`
	CreatedAt  time.Time `json:"created_at"`
}

func ToItemLink(l store.ItemLink) ItemLink {
	return ItemLink{
		ID: l.ID.String(), FromItemID: l.FromItemID.String(), ToItemID: l.ToItemID.String(),
		Kind: l.Kind, CreatedAt: l.CreatedAt,
	}
}

func ToItemLinks(ls []store.ItemLink) []ItemLink {
	out := make([]ItemLink, len(ls))
	for i, l := range ls {
		out[i] = ToItemLink(l)
	}
	return out
}

// ItemRef grounds an item to where it lives in code (commit/file/pr/branch/url).
type ItemRef struct {
	ID        string    `json:"id"`
	ItemID    string    `json:"item_id"`
	Kind      string    `json:"kind"`
	Ref       string    `json:"ref"`
	Label     string    `json:"label,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func ToItemRef(r store.ItemRef) ItemRef {
	return ItemRef{
		ID: r.ID.String(), ItemID: r.ItemID.String(), Kind: r.Kind, Ref: r.Ref,
		Label: r.Label, CreatedAt: r.CreatedAt,
	}
}

func ToItemRefs(rs []store.ItemRef) []ItemRef {
	out := make([]ItemRef, len(rs))
	for i, r := range rs {
		out[i] = ToItemRef(r)
	}
	return out
}

// ItemWithProject is an item carrying its project slug, for cross-project views.
type ItemWithProject struct {
	Item
	ProjectSlug string `json:"project_slug"`
}

// NextAction is the ranked "what should I work on" list of ready (unblocked) items.
type NextAction struct {
	Items []ItemWithProject `json:"items"`
}

func ReadyRowToItem(r store.ListReadyItemsRow) ItemWithProject {
	return ItemWithProject{Item: ToItem(store.Item{
		ID: r.ID, Ref: r.Ref, ProjectID: r.ProjectID, Type: r.Type, Title: r.Title, Body: r.Body,
		Status: r.Status, Priority: r.Priority, Assignee: r.Assignee, Position: r.Position,
		Source: r.Source, ExternalRef: r.ExternalRef, Tags: r.Tags, Metadata: r.Metadata,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt,
	}), ProjectSlug: r.ProjectSlug}
}

// TopOpenRowToItem converts a windowed global-view row into an Item, truncating
// the body to maxBody runes (0 = full).
func TopOpenRowToItem(r store.ListTopOpenItemsPerProjectRow, maxBody int) Item {
	return ToItemTrunc(store.Item{
		ID: r.ID, Ref: r.Ref, ProjectID: r.ProjectID, Type: r.Type, Title: r.Title, Body: r.Body,
		Status: r.Status, Priority: r.Priority, Assignee: r.Assignee, Position: r.Position,
		Source: r.Source, ExternalRef: r.ExternalRef, Tags: r.Tags, Metadata: r.Metadata,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt,
	}, maxBody)
}

func StaleInProgressToItem(r store.ListStaleInProgressRow) ItemWithProject {
	return ItemWithProject{Item: ToItem(store.Item{
		ID: r.ID, Ref: r.Ref, ProjectID: r.ProjectID, Type: r.Type, Title: r.Title, Body: r.Body,
		Status: r.Status, Priority: r.Priority, Assignee: r.Assignee, Position: r.Position,
		Source: r.Source, ExternalRef: r.ExternalRef, Tags: r.Tags, Metadata: r.Metadata,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt,
	}), ProjectSlug: r.ProjectSlug}
}

func UntriagedBugToItem(r store.ListUntriagedBugsRow) ItemWithProject {
	return ItemWithProject{Item: ToItem(store.Item{
		ID: r.ID, Ref: r.Ref, ProjectID: r.ProjectID, Type: r.Type, Title: r.Title, Body: r.Body,
		Status: r.Status, Priority: r.Priority, Assignee: r.Assignee, Position: r.Position,
		Source: r.Source, ExternalRef: r.ExternalRef, Tags: r.Tags, Metadata: r.Metadata,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt,
	}), ProjectSlug: r.ProjectSlug}
}

// Digest is a compact rollup of a project's recent activity since a timestamp:
// counts by kind, the actual decisions, and the current summary — cheap for an
// agent to read instead of replaying the full activity log.
type Digest struct {
	Project        string         `json:"project"`
	Since          time.Time      `json:"since"`
	Counts         map[string]int `json:"counts"`
	ItemsTouched   int            `json:"items_touched"`
	Decisions      []Activity     `json:"decisions"`
	CurrentSummary string         `json:"current_summary"`
}

// StaleReport surfaces things a housekeeping agent should look at.
type StaleReport struct {
	StaleInProgress []ItemWithProject `json:"stale_in_progress"`
	UntriagedBugs   []ItemWithProject `json:"untriaged_bugs"`
	StaleSummaries  []StaleSummary    `json:"stale_summaries"`
}

// StaleSummary is an active project whose summary predates its latest activity.
type StaleSummary struct {
	Slug             string    `json:"slug"`
	Name             string    `json:"name"`
	SummaryUpdatedAt time.Time `json:"summary_updated_at"`
	LastActivity     time.Time `json:"last_activity"`
}

func ToStaleSummary(r store.ListStaleProjectSummariesRow) StaleSummary {
	return StaleSummary{
		Slug: r.Slug, Name: r.Name, SummaryUpdatedAt: r.UpdatedAt, LastActivity: r.LastActivity,
	}
}

// Webhook is a registered subscriber. The secret is never serialized.
type Webhook struct {
	ID        string    `json:"id"`
	ProjectID *string   `json:"project_id,omitempty"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
}

func ToWebhook(w store.Webhook) Webhook {
	var projectID *string
	if w.ProjectID.Valid {
		s := uuid.UUID(w.ProjectID.Bytes).String()
		projectID = &s
	}
	events := w.Events
	if events == nil {
		events = []string{}
	}
	return Webhook{
		ID: w.ID.String(), ProjectID: projectID, URL: w.Url, Events: events,
		Active: w.Active, CreatedAt: w.CreatedAt,
	}
}

func ToWebhooks(ws []store.Webhook) []Webhook {
	out := make([]Webhook, len(ws))
	for i, w := range ws {
		out[i] = ToWebhook(w)
	}
	return out
}

// WebhookEvent is an outbox row — for operator visibility into delivery state
// (pending, delivered, or dead-lettered with a last_error).
type WebhookEvent struct {
	ID            string     `json:"id"`
	ProjectID     *string    `json:"project_id,omitempty"`
	Event         string     `json:"event"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt time.Time  `json:"next_attempt_at"`
	DeliveredAt   *time.Time `json:"delivered_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

func ToWebhookEvent(e store.WebhookEvent) WebhookEvent {
	var projectID *string
	if e.ProjectID.Valid {
		s := uuid.UUID(e.ProjectID.Bytes).String()
		projectID = &s
	}
	return WebhookEvent{
		ID: e.ID.String(), ProjectID: projectID, Event: e.Event, Attempts: int(e.Attempts),
		NextAttemptAt: e.NextAttemptAt, DeliveredAt: e.DeliveredAt, LastError: e.LastError, CreatedAt: e.CreatedAt,
	}
}

func ToWebhookEvents(es []store.WebhookEvent) []WebhookEvent {
	out := make([]WebhookEvent, len(es))
	for i, e := range es {
		out[i] = ToWebhookEvent(e)
	}
	return out
}
