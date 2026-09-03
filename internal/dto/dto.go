// Package dto holds the clean, agent-friendly JSON shapes shared by the HTTP
// API and the MCP server. They drop the generated tsvector `search` column and
// other internals, keeping payloads compact — the whole point of the project is
// orienting an agent for as few tokens as possible.
//
// UUIDs are rendered as strings and metadata as objects so the MCP SDK's
// generated output schemas match the marshaled values (uuid.UUID infers as a
// byte array, json.RawMessage as a string — both would fail schema validation).
package dto

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"flightdeck/internal/store"
)

func toMeta(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || len(m) == 0 {
		return nil
	}
	return m
}

type Project struct {
	ID           string    `json:"id"`
	Slug         string    `json:"slug"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Summary      string    `json:"summary"`
	Instructions string    `json:"instructions,omitempty"`
	Aliases      []string  `json:"aliases,omitempty"`
	RepoURL      *string   `json:"repo_url,omitempty"`
	SiteURL      *string   `json:"site_url,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func ToProject(p store.Project) Project {
	var aliases []string
	if len(p.Aliases) > 0 {
		aliases = p.Aliases
	}
	return Project{
		ID: p.ID.String(), Slug: p.Slug, Name: p.Name, Status: p.Status, Summary: p.Summary,
		Instructions: p.Instructions, Aliases: aliases, RepoURL: p.RepoUrl, SiteURL: p.SiteUrl,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func ToProjects(ps []store.Project) []Project {
	out := make([]Project, len(ps))
	for i, p := range ps {
		out[i] = ToProject(p)
	}
	return out
}

type Item struct {
	ID string `json:"id"`
	// Ref is the short, stable handle (e.g. "finforge-42") accepted anywhere an
	// id is — cheaper for an agent to carry across calls than the UUID.
	Ref         string         `json:"ref"`
	ProjectID   string         `json:"project_id"`
	Type        string         `json:"type"`
	Title       string         `json:"title"`
	Body        string         `json:"body"`
	Status      string         `json:"status"`
	Priority    string         `json:"priority"`
	Assignee    *string        `json:"assignee,omitempty"`
	Position    float64        `json:"position"`
	Source      string         `json:"source"`
	ExternalRef *string        `json:"external_ref,omitempty"`
	Tags        []string       `json:"tags"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	ClosedAt    *time.Time     `json:"closed_at,omitempty"`
	// Version is the optimistic-concurrency counter — pass it back as
	// expected_version on update to detect a concurrent edit (conflict).
	Version int `json:"version"`
	// Blocked annotations are populated only by orient / next_action views.
	Blocked   bool     `json:"blocked,omitempty"`
	BlockedBy []string `json:"blocked_by,omitempty"`
	// AcceptanceCriteria is the definition-of-done checklist; AcceptanceUnmet is
	// the count of criteria not yet satisfied (the contract still owed).
	AcceptanceCriteria []Criterion `json:"acceptance_criteria,omitempty"`
	AcceptanceUnmet    int         `json:"acceptance_unmet,omitempty"`
	// Attachments (screenshots) are populated on single-item fetches only —
	// list views skip them to avoid an N+1.
	Attachments []Attachment `json:"attachments,omitempty"`
}

// Criterion is one definition-of-done checklist entry on an item.
type Criterion struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

func toCriteria(raw json.RawMessage) ([]Criterion, int) {
	if len(raw) == 0 {
		return nil, 0
	}
	var cs []Criterion
	if err := json.Unmarshal(raw, &cs); err != nil || len(cs) == 0 {
		return nil, 0
	}
	unmet := 0
	for _, c := range cs {
		if !c.Done {
			unmet++
		}
	}
	return cs, unmet
}

func ToItem(i store.Item) Item { return ToItemTrunc(i, 0) }

// ToItemTrunc converts an item, truncating the body to maxBody runes (0 = full).
// Truncated bodies carry a trailing ellipsis so an agent knows to fetch the full
// item via get_item; everything else is preserved.
func ToItemTrunc(i store.Item, maxBody int) Item {
	tags := i.Tags
	if tags == nil {
		tags = []string{}
	}
	criteria, unmet := toCriteria(i.AcceptanceCriteria)
	return Item{
		ID: i.ID.String(), Ref: i.Ref, ProjectID: i.ProjectID.String(), Type: i.Type, Title: i.Title, Body: trunc(i.Body, maxBody),
		Status: i.Status, Priority: i.Priority, Assignee: i.Assignee, Position: i.Position,
		Source: i.Source, ExternalRef: i.ExternalRef, Tags: tags, Metadata: toMeta(i.Metadata),
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt, ClosedAt: i.ClosedAt, Version: int(i.Version),
		AcceptanceCriteria: criteria, AcceptanceUnmet: unmet,
	}
}

func ToItems(items []store.Item) []Item { return ToItemsTrunc(items, 0) }

func ToItemsTrunc(items []store.Item, maxBody int) []Item {
	out := make([]Item, len(items))
	for i, it := range items {
		out[i] = ToItemTrunc(it, maxBody)
	}
	return out
}

// trunc shortens s to max runes (0 = no limit), appending "…" when it cut.
func trunc(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// ItemBrief is the lightest item shape — ref/title/status/priority — for "what's
// next" lists where full item objects would just duplicate tokens.
type ItemBrief struct {
	Ref      string `json:"ref"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	Type     string `json:"type"`
}

func ToItemBrief(i Item) ItemBrief {
	return ItemBrief{Ref: i.Ref, Title: i.Title, Status: i.Status, Priority: i.Priority, Type: i.Type}
}

type Activity struct {
	ID         string         `json:"id"`
	ProjectID  string         `json:"project_id"`
	ItemID     *string        `json:"item_id,omitempty"`
	Kind       string         `json:"kind"`
	Actor      string         `json:"actor"`
	Body       string         `json:"body"`
	Confidence string         `json:"confidence,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

func ToActivity(a store.Activity) Activity {
	var itemID *string
	if a.ItemID.Valid {
		s := uuid.UUID(a.ItemID.Bytes).String()
		itemID = &s
	}
	confidence := a.Confidence
	if confidence == "unspecified" {
		confidence = "" // omit the default so payloads stay compact
	}
	return Activity{
		ID: a.ID.String(), ProjectID: a.ProjectID.String(), ItemID: itemID, Kind: a.Kind,
		Actor: a.Actor, Body: a.Body, Confidence: confidence, Metadata: toMeta(a.Metadata), CreatedAt: a.CreatedAt,
	}
}

func ToActivities(as []store.Activity) []Activity { return ToActivitiesTrunc(as, 0) }

func ToActivitiesTrunc(as []store.Activity, maxBody int) []Activity {
	out := make([]Activity, len(as))
	for i, a := range as {
		v := ToActivity(a)
		v.Body = trunc(v.Body, maxBody)
		out[i] = v
	}
	return out
}

// ToSearchItems converts ranked item search rows into Item DTOs.
func ToSearchItems(rows []store.SearchItemsRow) []Item { return ToSearchItemsTrunc(rows, 0) }

func ToSearchItemsTrunc(rows []store.SearchItemsRow, maxBody int) []Item {
	out := make([]Item, len(rows))
	for i, r := range rows {
		out[i] = ToItemTrunc(store.Item{
			ID: r.ID, Ref: r.Ref, ProjectID: r.ProjectID, Type: r.Type, Title: r.Title, Body: r.Body,
			Status: r.Status, Priority: r.Priority, Assignee: r.Assignee, Position: r.Position,
			Source: r.Source, ExternalRef: r.ExternalRef, Tags: r.Tags, Metadata: r.Metadata,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt,
		}, maxBody)
	}
	return out
}

// ToSemanticItemsTrunc converts vector (cosine) search rows into Item DTOs,
// preserving the nearest-neighbour order the query returned them in.
func ToSemanticItemsTrunc(rows []store.SearchItemsSemanticRow, maxBody int) []Item {
	out := make([]Item, len(rows))
	for i, r := range rows {
		out[i] = ToItemTrunc(store.Item{
			ID: r.ID, Ref: r.Ref, ProjectID: r.ProjectID, Type: r.Type, Title: r.Title, Body: r.Body,
			Status: r.Status, Priority: r.Priority, Assignee: r.Assignee, Position: r.Position,
			Source: r.Source, ExternalRef: r.ExternalRef, Tags: r.Tags, Metadata: r.Metadata,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt, Version: r.Version,
			AcceptanceCriteria: r.AcceptanceCriteria,
		}, maxBody)
	}
	return out
}

// ToFuzzyItemsTrunc converts trigram fuzzy-search rows into Item DTOs.
func ToFuzzyItemsTrunc(rows []store.SearchItemsFuzzyRow, maxBody int) []Item {
	out := make([]Item, len(rows))
	for i, r := range rows {
		out[i] = ToItemTrunc(store.Item{
			ID: r.ID, Ref: r.Ref, ProjectID: r.ProjectID, Type: r.Type, Title: r.Title, Body: r.Body,
			Status: r.Status, Priority: r.Priority, Assignee: r.Assignee, Position: r.Position,
			Source: r.Source, ExternalRef: r.ExternalRef, Tags: r.Tags, Metadata: r.Metadata,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt, Version: r.Version,
		}, maxBody)
	}
	return out
}

func ToSearchActivities(rows []store.SearchActivityRow) []Activity {
	return ToSearchActivitiesTrunc(rows, 0)
}

func ToSearchActivitiesTrunc(rows []store.SearchActivityRow, maxBody int) []Activity {
	out := make([]Activity, len(rows))
	for i, r := range rows {
		v := ToActivity(store.Activity{
			ID: r.ID, ProjectID: r.ProjectID, ItemID: r.ItemID, Kind: r.Kind,
			Actor: r.Actor, Body: r.Body, Metadata: r.Metadata, CreatedAt: r.CreatedAt,
		})
		v.Body = trunc(v.Body, maxBody)
		out[i] = v
	}
	return out
}

// ToSemanticActivitiesTrunc converts vector (cosine) activity search rows into
// Activity DTOs, preserving nearest-neighbour order.
func ToSemanticActivitiesTrunc(rows []store.SearchActivitySemanticRow, maxBody int) []Activity {
	out := make([]Activity, len(rows))
	for i, r := range rows {
		v := ToActivity(store.Activity{
			ID: r.ID, ProjectID: r.ProjectID, ItemID: r.ItemID, Kind: r.Kind,
			Actor: r.Actor, Body: r.Body, Metadata: r.Metadata, Confidence: r.Confidence, CreatedAt: r.CreatedAt,
		})
		v.Body = trunc(v.Body, maxBody)
		out[i] = v
	}
	return out
}

// ProjectContext is the compact orient bundle for a single project: its
// agent-maintained summary, open items, recent decisions, and status counts.
type ProjectContext struct {
	Project        Project        `json:"project"`
	OpenItems      []Item         `json:"open_items"`
	RecentActivity []Activity     `json:"recent_activity"`
	Counts         map[string]int `json:"counts,omitempty"`
	// ReadyNext folds in the top unblocked items ("what should I work on") so
	// orient answers "where are we AND what's next" in a single call. Kept as
	// briefs (ref+title) to avoid duplicating full item bodies already in
	// open_items.
	ReadyNext []ItemBrief `json:"ready_next,omitempty"`
	// Nudges are short housekeeping prompts (stale in-progress work, a summary
	// that's drifted) — making the cost of NOT logging visible on every orient.
	Nudges []string `json:"nudges,omitempty"`
	// RejectedApproaches surfaces tried-and-abandoned / out-of-scope notes so an
	// agent doesn't rebuild dead work.
	RejectedApproaches []Activity `json:"rejected_approaches,omitempty"`
	// Trust signals on the summary: when it was last written and how much has
	// happened since (high count = treat the summary as stale).
	SummaryUpdatedAt       time.Time `json:"summary_updated_at"`
	ActivitiesSinceSummary int       `json:"activities_since_summary"`
}

// ProjectOverview is one project's slice of the horizontal global view. Top
// items are briefs (ref/title/status/priority/type), not full Items: the global
// view is a map, and an agent drills into a project (get_project_context) or an
// item (get_item) for bodies — full items here multiplied the payload ~5× for
// tokens nobody read.
type ProjectOverview struct {
	Project  Project        `json:"project"`
	Counts   map[string]int `json:"counts,omitempty"`
	TopItems []ItemBrief    `json:"top_items"`
}

// GlobalContext is the "load the map" payload across all active projects.
type GlobalContext struct {
	Projects []ProjectOverview `json:"projects"`
	// Notices are instance-level lines (e.g. "a newer flightdeck is released")
	// surfaced at orient time, mirroring ProjectContext.Nudges.
	Notices []string `json:"notices,omitempty"`
}

// SearchResults bundles ranked item and activity matches for a FTS query.
type SearchResults struct {
	Query    string     `json:"query"`
	Items    []Item     `json:"items"`
	Activity []Activity `json:"activity"`
}

// ContextImpactEvent is an agent-reported outcome attributed to retrieved
// Flightdeck context. It is evidence for usefulness analysis, not causal proof.
type ContextImpactEvent struct {
	ID                    string    `json:"id"`
	RecordedAt            time.Time `json:"recorded_at"`
	Actor                 string    `json:"actor"`
	SessionID             string    `json:"session_id"`
	Project               string    `json:"project"`
	Item                  *string   `json:"item,omitempty"`
	Effect                string    `json:"effect"`
	Mechanism             string    `json:"mechanism"`
	ContextRefs           []string  `json:"context_refs"`
	Evidence              string    `json:"evidence"`
	EstimatedMinutesDelta *int32    `json:"estimated_minutes_delta,omitempty"`
}

func ToContextImpactEvent(event store.ContextImpactEvent) ContextImpactEvent {
	refs := event.ContextRefs
	if refs == nil {
		refs = []string{}
	}
	return ContextImpactEvent{
		ID: event.ID.String(), RecordedAt: event.RecordedAt, Actor: event.Actor,
		SessionID: event.SessionID, Project: event.Project, Item: event.Item,
		Effect: event.Effect, Mechanism: event.Mechanism, ContextRefs: refs,
		Evidence: event.Evidence, EstimatedMinutesDelta: event.EstimatedMinutesDelta,
	}
}

func ToContextImpactEvents(events []store.ContextImpactEvent) []ContextImpactEvent {
	out := make([]ContextImpactEvent, len(events))
	for i, event := range events {
		out[i] = ToContextImpactEvent(event)
	}
	return out
}

// ItemsPage is a paginated slice of items. NextOffset is non-nil when more rows
// remain — pass it back as the cursor to fetch the following page.
type ItemsPage struct {
	Items      []Item `json:"items"`
	NextOffset *int   `json:"next_offset,omitempty"`
}

// UsageReport summarizes how agents used flightdeck over a window — per-tool
// stats, unused tools, daily volume, top projects, and search quality — so the
// service can be improved from observed behavior.
type UsageReport struct {
	Days        int         `json:"days"`
	Since       time.Time   `json:"since"`
	TotalCalls  int         `json:"total_calls"`
	TotalErrors int         `json:"total_errors"`
	Tools       []ToolUsage `json:"tools"`
	// UnusedTools are registered tools with zero calls in the window —
	// candidates for removal, or a sign agents don't know they exist.
	UnusedTools  []string       `json:"unused_tools,omitempty"`
	TopProjects  []ProjectCalls `json:"top_projects,omitempty"`
	Daily        []DayCalls     `json:"daily,omitempty"`
	RecentErrors []ToolError    `json:"recent_errors,omitempty"`
	Search       SearchUsage    `json:"search"`
	// Coverage reports how much of the corpus is actually embedded — the ceiling
	// on what semantic search can ever return.
	Coverage EmbeddingCoverage `json:"embedding_coverage"`
}

// EmbeddingCoverage is the semantic tier's backfill health. A low embedded
// fraction (or a growing failed count) explains a semantic tier that rescues
// nothing regardless of tuning.
type EmbeddingCoverage struct {
	ItemsTotal       int `json:"items_total"`
	ItemsEmbedded    int `json:"items_embedded"`
	ItemsFailed      int `json:"items_failed"`
	ActivityTotal    int `json:"activity_total"`
	ActivityEmbedded int `json:"activity_embedded"`
}

// ToolUsage is one tool's stats. AvgResultKB approximates the token cost an
// agent pays per call.
type ToolUsage struct {
	Tool        string    `json:"tool"`
	Calls       int       `json:"calls"`
	Errors      int       `json:"errors,omitempty"`
	P50Ms       float64   `json:"p50_ms"`
	P95Ms       float64   `json:"p95_ms"`
	AvgResultKB float64   `json:"avg_result_kb"`
	LastUsed    time.Time `json:"last_used"`
}

type ProjectCalls struct {
	Project string `json:"project"`
	Calls   int    `json:"calls"`
}

type DayCalls struct {
	Day    string `json:"day"`
	Calls  int    `json:"calls"`
	Errors int    `json:"errors,omitempty"`
}

type ToolError struct {
	Tool  string    `json:"tool"`
	Error string    `json:"error"`
	At    time.Time `json:"at"`
}

// SearchUsage reports search quality: zero-result queries are recall gaps;
// rescues count searches where a fallback tier saved a lexical miss.
type SearchUsage struct {
	Searches          int      `json:"searches"`
	ZeroResult        int      `json:"zero_result"`
	SemanticRescues   int      `json:"semantic_rescues"`
	TrigramRescues    int      `json:"trigram_rescues"`
	AvgReturned       float64  `json:"avg_returned"`
	ZeroResultQueries []string `json:"zero_result_queries,omitempty"`
}
