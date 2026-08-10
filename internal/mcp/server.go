// Package mcp exposes the flightdeck store over a streamable-HTTP MCP server.
// The tools are thin wrappers over the same store/service layer the HTTP API
// uses — this is the agent contract, and the write tools are what keep the data
// fresh as a side effect of agents doing work.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"flightdeck/internal/auth"
	"flightdeck/internal/dto"
	"flightdeck/internal/metrics"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

type handlers struct {
	st  *store.Store
	svc *service.Service
	// toolNames is every registered tool, collected by addTool so usage_report
	// can flag registered-but-never-called tools.
	toolNames []string
}

// addTool registers a tool and records its name in h.toolNames.
//
// Handlers return a plain DTO which is marshaled into a single TextContent
// block. Returning it as the SDK's typed Out would emit the same JSON twice —
// once as structuredContent and again as the spec-suggested text copy — and
// hang a generated output schema on every tools/list entry. Skipping both
// halves the payload of every tool result (usage_report showed
// get_global_context averaging 85 KB) and keeps the manifest lean; the text
// JSON is all an agent reads anyway.
func addTool[In, Out any](h *handlers, s *mcpsdk.Server, t *mcpsdk.Tool, fn func(context.Context, *mcpsdk.CallToolRequest, In) (Out, error)) {
	mcpsdk.AddTool(s, t, func(ctx context.Context, req *mcpsdk.CallToolRequest, in In) (*mcpsdk.CallToolResult, any, error) {
		out, err := fn(ctx, req, in)
		if err != nil {
			return nil, nil, err
		}
		b, err := json.Marshal(out)
		if err != nil {
			return nil, nil, err
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(b)}}}, nil, nil
	})
	h.toolNames = append(h.toolNames, t.Name)
}

// NewHandler builds the MCP server, registers all tools, and returns an
// http.Handler serving the streamable-HTTP transport (mount it at /mcp).
func NewHandler(st *store.Store, svc *service.Service, version string) http.Handler {
	h := &handlers{st: st, svc: svc}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:    "flightdeck",
		Version: version,
	}, nil)
	h.register(server)
	server.AddReceivingMiddleware(metricsMiddleware)
	server.AddReceivingMiddleware(h.usageMiddleware)
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil)
}

// metricsMiddleware records per-tool RED metrics for every tools/call.
func metricsMiddleware(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		name := "unknown"
		if p, ok := req.GetParams().(*mcpsdk.CallToolParamsRaw); ok {
			name = p.Name
		}
		start := time.Now()
		res, err := next(ctx, method, req)
		metrics.MCP(name, err == nil, time.Since(start))
		return res, err
	}
}

// usageMiddleware records every tools/call into the tool_calls analytics table:
// which tool, by whom, against which project, with what args, how long it took,
// and how big the result was (the token-cost proxy). Best-effort by design —
// RecordToolCall swallows its own failures.
func (h *handlers) usageMiddleware(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
	return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		call := service.ToolCall{Tool: "unknown", Actor: auth.Actor(ctx)}
		if p, ok := req.GetParams().(*mcpsdk.CallToolParamsRaw); ok {
			call.Tool = p.Name
			call.Args = p.Arguments
			// Shallow-probe the args for the project slug most tools carry.
			var probe struct {
				Project string `json:"project"`
				Slug    string `json:"slug"`
			}
			_ = json.Unmarshal(p.Arguments, &probe)
			if call.Project = probe.Project; call.Project == "" {
				call.Project = probe.Slug
			}
		}
		start := time.Now()
		res, err := next(ctx, method, req)
		call.Duration = time.Since(start)
		call.OK = err == nil
		if err != nil {
			call.Err = err.Error()
		} else if b, merr := json.Marshal(res); merr == nil {
			call.ResultBytes = len(b)
		}
		h.svc.RecordToolCall(ctx, call)
		return res, err
	}
}

func ptr[T any](v T) *T { return &v }

func optStr(p *string) *string {
	if p == nil || *p == "" {
		return nil
	}
	return p
}

func metaJSON(m map[string]any) json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

func criteriaJSON(cs []criterionIn) json.RawMessage {
	if len(cs) == 0 {
		return nil
	}
	b, err := json.Marshal(cs)
	if err != nil {
		return nil
	}
	return b
}

func (h *handlers) resolveProject(ctx context.Context, slug string) (uuid.UUID, error) {
	p, err := h.st.GetProjectBySlug(ctx, slug)
	if err != nil {
		return uuid.Nil, fmt.Errorf("project %q not found", slug)
	}
	return p.ID, nil
}

// resolveItemRef accepts either an item UUID or a short handle (e.g. "finforge-42")
// and returns the item's UUID — so an agent can pass whichever it has on hand.
func (h *handlers) resolveItemRef(ctx context.Context, ref string) (uuid.UUID, error) {
	if id, err := uuid.Parse(ref); err == nil {
		return id, nil
	}
	item, err := h.st.GetItemByRef(ctx, ref)
	if err != nil {
		return uuid.Nil, fmt.Errorf("no item with id or ref %q", ref)
	}
	return item.ID, nil
}

// verbosityOf resolves the optional verbosity arg; agents default to compact
// (truncated bodies) for token economy.
func verbosityOf(p *string) service.Verbosity {
	if p != nil && *p == string(service.VerbosityFull) {
		return service.VerbosityFull
	}
	return service.VerbosityCompact
}

// --- tool I/O types ---

type listProjectsIn struct {
	Status *string `json:"status,omitempty" jsonschema:"filter by status: active|paused|done|archived"`
}
type projectsOut struct {
	Projects []dto.Project `json:"projects"`
}

type slugIn struct {
	Slug      string  `json:"slug" jsonschema:"project slug, e.g. finforge"`
	Verbosity *string `json:"verbosity,omitempty" jsonschema:"compact (default; item/activity bodies truncated for token economy) | full (untruncated)"`
}

type globalIn struct {
	Verbosity *string `json:"verbosity,omitempty" jsonschema:"compact (default) | full (adds each project's instructions)"`
}

type searchIn struct {
	Query     string  `json:"query" jsonschema:"full-text query over items and activity"`
	Project   *string `json:"project,omitempty" jsonschema:"limit to a project slug"`
	Type      *string `json:"type,omitempty" jsonschema:"limit items to a type: task|bug|idea|note"`
	Limit     *int    `json:"limit,omitempty" jsonschema:"max item matches (default 25)"`
	Verbosity *string `json:"verbosity,omitempty" jsonschema:"compact (default) | full"`
}

type listItemsIn struct {
	Project      *string `json:"project,omitempty" jsonschema:"project slug"`
	Status       *string `json:"status,omitempty" jsonschema:"backlog|todo|in_progress|blocked|done|wontfix"`
	Type         *string `json:"type,omitempty" jsonschema:"task|bug|idea|note"`
	Assignee     *string `json:"assignee,omitempty"`
	Tag          *string `json:"tag,omitempty" jsonschema:"single tag to filter by"`
	UpdatedSince *string `json:"updated_since,omitempty" jsonschema:"RFC3339 timestamp"`
	Limit        *int    `json:"limit,omitempty" jsonschema:"page size (default 50, max 500)"`
	Cursor       *int    `json:"cursor,omitempty" jsonschema:"offset cursor from a prior page's next_offset"`
	Verbosity    *string `json:"verbosity,omitempty" jsonschema:"compact (default; bodies truncated) | full"`
}

type getItemIn struct {
	ID string `json:"id" jsonschema:"item UUID or short ref (e.g. finforge-42)"`
}

// criterionIn is one definition-of-done checklist entry.
type criterionIn struct {
	Text string `json:"text"`
	Done bool   `json:"done,omitempty"`
}

type createItemIn struct {
	Project            string         `json:"project" jsonschema:"project slug to create the item under"`
	Type               *string        `json:"type,omitempty" jsonschema:"task|bug|idea|note (default task)"`
	Title              string         `json:"title"`
	Body               *string        `json:"body,omitempty"`
	Priority           *string        `json:"priority,omitempty" jsonschema:"low|med|high|urgent (default med)"`
	Status             *string        `json:"status,omitempty" jsonschema:"backlog|todo|in_progress|blocked|done|wontfix"`
	Tags               []string       `json:"tags,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
	IdempotencyKey     *string        `json:"idempotency_key,omitempty" jsonschema:"supply a stable key to make creation safe to retry — a prior item with the same key is returned instead of duplicated"`
	AcceptanceCriteria []criterionIn  `json:"acceptance_criteria,omitempty" jsonschema:"definition-of-done checklist: the contract to satisfy before marking done"`
}

type updateItemIn struct {
	ID                 string         `json:"id" jsonschema:"item UUID or short ref (e.g. finforge-42)"`
	Title              *string        `json:"title,omitempty" jsonschema:"new title (e.g. to fix a typo)"`
	Type               *string        `json:"type,omitempty" jsonschema:"reclassify: task|bug|idea|note"`
	Status             *string        `json:"status,omitempty"`
	Priority           *string        `json:"priority,omitempty"`
	Assignee           *string        `json:"assignee,omitempty" jsonschema:"empty string clears the assignee"`
	Body               *string        `json:"body,omitempty" jsonschema:"empty string clears the body"`
	Tags               []string       `json:"tags,omitempty"`
	Position           *float64       `json:"position,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
	AcceptanceCriteria []criterionIn  `json:"acceptance_criteria,omitempty" jsonschema:"replace the definition-of-done checklist (e.g. to tick criteria done)"`
	ExpectedVersion    *int           `json:"expected_version,omitempty" jsonschema:"optimistic lock: the version you read; the update is rejected with a conflict if the item changed since"`
}

type createItemsIn struct {
	Items []createItemIn `json:"items" jsonschema:"items to create in one call"`
}
type createItemsOut struct {
	Items []dto.Item `json:"items"`
}

type logActivityIn struct {
	Project    string         `json:"project" jsonschema:"project slug"`
	Item       *string        `json:"item,omitempty" jsonschema:"optional item UUID or short ref (e.g. finforge-42) this activity is about"`
	Kind       string         `json:"kind" jsonschema:"decision|progress|status_change|comment|created|rejected — use 'rejected' for approaches tried and abandoned / out of scope"`
	Body       string         `json:"body" jsonschema:"the note — capture the why, not just the what"`
	Confidence *string        `json:"confidence,omitempty" jsonschema:"unspecified|inferred|confirmed — mark human-confirmed ground truth vs agent-inferred claims"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

type addRefIn struct {
	Item  string  `json:"item" jsonschema:"item UUID or short ref (e.g. finforge-42) to ground"`
	Kind  *string `json:"kind,omitempty" jsonschema:"commit|file|pr|branch|url (default url)"`
	Ref   string  `json:"ref" jsonschema:"the reference value — a SHA, file path, PR url, branch name, or url"`
	Label *string `json:"label,omitempty" jsonschema:"optional human label"`
}
type listRefsIn struct {
	Item string `json:"item" jsonschema:"item UUID or short ref (e.g. finforge-42)"`
}
type refsOut struct {
	Refs []dto.ItemRef `json:"refs"`
}
type refOut struct {
	Ref dto.ItemRef `json:"ref"`
}

type updateSummaryIn struct {
	Slug    string `json:"slug" jsonschema:"project slug"`
	Summary string `json:"summary" jsonschema:"the new current-state paragraph for the project"`
}

type createProjectIn struct {
	Slug         string   `json:"slug" jsonschema:"unique short slug, e.g. finforge"`
	Name         string   `json:"name" jsonschema:"display name"`
	Status       *string  `json:"status,omitempty" jsonschema:"active|paused|done|archived (default active)"`
	Summary      *string  `json:"summary,omitempty" jsonschema:"one-paragraph current-state summary"`
	Instructions *string  `json:"instructions,omitempty" jsonschema:"project-specific conventions agents should follow"`
	Aliases      []string `json:"aliases,omitempty" jsonschema:"alternate names/repo-dir names so resolve_project can match this project from a path (e.g. Survivor50Draft)"`
	RepoURL      *string  `json:"repo_url,omitempty"`
	SiteURL      *string  `json:"site_url,omitempty"`
}

type setInstructionsIn struct {
	Slug         string `json:"slug" jsonschema:"project slug"`
	Instructions string `json:"instructions" jsonschema:"project-specific conventions agents should follow (e.g. run tests before marking done)"`
}

type linkItemsIn struct {
	From string `json:"from" jsonschema:"source item UUID or short ref (e.g. finforge-42)"`
	To   string `json:"to" jsonschema:"target item UUID or short ref (e.g. finforge-43)"`
	Kind string `json:"kind" jsonschema:"blocks|relates_to|parent_of (default relates_to); 'blocks' means 'from' blocks 'to'"`
}

type resolveProjectIn struct {
	Path string `json:"path" jsonschema:"a filesystem path (e.g. your cwd) or git remote URL; resolves to the project you're standing in"`
}

type completeItemIn struct {
	Item    string  `json:"item" jsonschema:"item UUID or short ref (e.g. finforge-42) to close"`
	Why     string  `json:"why" jsonschema:"the decision/outcome to record — why this is done, captured as a decision activity"`
	Summary *string `json:"summary,omitempty" jsonschema:"optional refreshed project summary to set in the same call"`
}

type unlinkItemsIn struct {
	ID string `json:"id" jsonschema:"item-link UUID to remove"`
}
type linkOut struct {
	Link dto.ItemLink `json:"link"`
}
type okOut struct {
	OK bool `json:"ok"`
}

type usageReportIn struct {
	Days *int `json:"days,omitempty" jsonschema:"lookback window in days (default 7, max 90)"`
}

// --- registration ---

func (h *handlers) register(s *mcpsdk.Server) {
	addTool(h, s, &mcpsdk.Tool{
		Name:        "list_projects",
		Description: "List projects, optionally filtered by status.",
	}, h.listProjects)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "get_project_context",
		Description: "Primary orient call: a project's summary, open items, recent decisions, and status counts.",
	}, h.getProjectContext)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "get_global_context",
		Description: "Horizontal 'load the map' view across all active projects (summary + counts + top items).",
	}, h.getGlobalContext)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "search",
		Description: "Search items and activity. Cascades full-text → semantic (meaning-based, vector) → trigram (typos/fragments) recall.",
	}, h.search)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "list_items",
		Description: "List items with optional filters (project, status, type, assignee, tag, updated_since).",
	}, h.listItems)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "get_item",
		Description: "Fetch a single item by UUID.",
	}, h.getItem)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "create_item",
		Description: "Create an item (task/bug/idea/note) under a project. Also appends a 'created' activity.",
	}, h.createItem)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "create_items",
		Description: "Create several items in one call (e.g. file a batch of tasks). Each gets its own 'created' activity and event.",
	}, h.createItems)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "update_item",
		Description: "Update an item. A status change automatically appends a 'status_change' activity.",
	}, h.updateItem)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "log_activity",
		Description: "Record a decision/progress/comment — the freshness flywheel. Capture the why.",
	}, h.logActivity)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "update_project_summary",
		Description: "Replace a project's current-state summary paragraph.",
	}, h.updateProjectSummary)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "create_project",
		Description: "Register a new tracked project (slug + name, optional status/summary/instructions/urls/aliases).",
	}, h.createProject)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "resolve_project",
		Description: "Identify the project from a filesystem path (your cwd) or git remote — no need to consult a slug table. Returns the matching project.",
	}, h.resolveProjectTool)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "complete_item",
		Description: "Close an item in one call: mark it done, record the why as a decision, and optionally refresh the project summary. The unit of work an agent actually finishes.",
	}, h.completeItem)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "set_project_instructions",
		Description: "Set a project's agent-instructions (conventions an agent reads on orient).",
	}, h.setProjectInstructions)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "link_items",
		Description: "Create a dependency/relationship between two items (blocks|relates_to|parent_of). 'blocks' affects what orient considers ready.",
	}, h.linkItems)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "unlink_items",
		Description: "Remove an item link by its UUID.",
	}, h.unlinkItems)

	// next_action, digest, and stale are intentionally NOT exposed as MCP tools:
	// get_project_context's nudges already surface the same "what's ready / what's
	// rotting" signal inline at orient time, so agents never reached for them
	// (zero calls in usage_report) — they only added token weight to the manifest.
	// The logic still lives in the service layer and is served over the HTTP API
	// (/next-action, /projects/{slug}/digest, /stale) for the web UI.

	addTool(h, s, &mcpsdk.Tool{
		Name:        "add_item_ref",
		Description: "Ground an item to where it lives in code: a commit SHA, file path, PR, branch, or url.",
	}, h.addItemRef)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "list_item_refs",
		Description: "List an item's code references (the 'where' for a piece of work).",
	}, h.listItemRefs)

	addTool(h, s, &mcpsdk.Tool{
		Name:        "usage_report",
		Description: "How agents used flightdeck over a window: per-tool call/error/latency/payload stats, unused tools, daily volume, top projects, and search quality (zero-result queries, semantic/trigram rescues). Use it to tune the service from observed behavior.",
	}, h.usageReport)
}

// --- tool implementations ---

func (h *handlers) listProjects(ctx context.Context, _ *mcpsdk.CallToolRequest, in listProjectsIn) (projectsOut, error) {
	projects, err := h.st.ListProjects(ctx, optStr(in.Status))
	if err != nil {
		return projectsOut{}, err
	}
	return projectsOut{Projects: dto.ToProjects(projects)}, nil
}

func (h *handlers) getProjectContext(ctx context.Context, _ *mcpsdk.CallToolRequest, in slugIn) (dto.ProjectContext, error) {
	bundle, err := h.svc.ProjectContext(ctx, in.Slug, verbosityOf(in.Verbosity))
	if err != nil {
		return dto.ProjectContext{}, err
	}
	return bundle, nil
}

func (h *handlers) getGlobalContext(ctx context.Context, _ *mcpsdk.CallToolRequest, in globalIn) (dto.GlobalContext, error) {
	bundle, err := h.svc.GlobalContext(ctx, verbosityOf(in.Verbosity))
	if err != nil {
		return dto.GlobalContext{}, err
	}
	return bundle, nil
}

func (h *handlers) search(ctx context.Context, _ *mcpsdk.CallToolRequest, in searchIn) (dto.SearchResults, error) {
	var projectID pgtype.UUID
	if in.Project != nil && *in.Project != "" {
		id, err := h.resolveProject(ctx, *in.Project)
		if err != nil {
			return dto.SearchResults{}, err
		}
		projectID = pgtype.UUID{Bytes: id, Valid: true}
	}
	var lim *int32
	if in.Limit != nil && *in.Limit > 0 {
		l := int32(*in.Limit)
		lim = &l
	}
	itemMax, actMax := service.BodyLimits(verbosityOf(in.Verbosity))
	items, acts, err := h.svc.SearchSmart(ctx, in.Query, projectID, optStr(in.Type), lim, itemMax, actMax)
	if err != nil {
		return dto.SearchResults{}, err
	}
	return dto.SearchResults{
		Query:    in.Query,
		Items:    items,
		Activity: acts,
	}, nil
}

func (h *handlers) listItems(ctx context.Context, _ *mcpsdk.CallToolRequest, in listItemsIn) (dto.ItemsPage, error) {
	var projectID pgtype.UUID
	if in.Project != nil && *in.Project != "" {
		id, err := h.resolveProject(ctx, *in.Project)
		if err != nil {
			return dto.ItemsPage{}, err
		}
		projectID = pgtype.UUID{Bytes: id, Valid: true}
	}
	var since *time.Time
	if in.UpdatedSince != nil && *in.UpdatedSince != "" {
		t, err := time.Parse(time.RFC3339, *in.UpdatedSince)
		if err != nil {
			return dto.ItemsPage{}, fmt.Errorf("invalid updated_since: %w", err)
		}
		since = &t
	}
	// Page size (default 50, max 500) and offset cursor. Fetch one extra row to
	// detect whether a further page exists.
	limit := 50
	if in.Limit != nil && *in.Limit > 0 {
		limit = *in.Limit
	}
	if limit > 500 {
		limit = 500
	}
	offset := 0
	if in.Cursor != nil && *in.Cursor > 0 {
		offset = *in.Cursor
	}
	fetch := int32(limit + 1)
	off := int32(offset)
	items, err := h.st.ListItems(ctx, store.ListItemsParams{
		ProjectID:    projectID,
		Status:       optStr(in.Status),
		Type:         optStr(in.Type),
		Assignee:     optStr(in.Assignee),
		Tag:          optStr(in.Tag),
		UpdatedSince: since,
		Lim:          &fetch,
		Off:          &off,
	})
	if err != nil {
		return dto.ItemsPage{}, err
	}
	var next *int
	if len(items) > limit {
		items = items[:limit]
		n := offset + limit
		next = &n
	}
	itemMax, _ := service.BodyLimits(verbosityOf(in.Verbosity))
	return dto.ItemsPage{Items: dto.ToItemsTrunc(items, itemMax), NextOffset: next}, nil
}

func (h *handlers) getItem(ctx context.Context, _ *mcpsdk.CallToolRequest, in getItemIn) (dto.Item, error) {
	id, err := h.resolveItemRef(ctx, in.ID)
	if err != nil {
		return dto.Item{}, err
	}
	item, err := h.st.GetItem(ctx, id)
	if err != nil {
		return dto.Item{}, err
	}
	out := dto.ToItem(item)
	// Surface screenshots as their MinIO reference (bucket/object_key) plus the
	// key-authed API URL, so an agent knows where to fetch the image.
	if atts, err := h.svc.ListAttachments(ctx, item.ID); err == nil && len(atts) > 0 {
		bucket := ""
		if b := h.svc.Blob(); b != nil {
			bucket = b.Bucket()
		}
		out.Attachments = dto.ToAttachments(atts, bucket)
	}
	return out, nil
}

func (h *handlers) createItem(ctx context.Context, _ *mcpsdk.CallToolRequest, in createItemIn) (dto.Item, error) {
	if err := service.ValidateItemFields(in.Type, in.Status, in.Priority); err != nil {
		return dto.Item{}, err
	}
	projectID, err := h.resolveProject(ctx, in.Project)
	if err != nil {
		return dto.Item{}, err
	}
	source := "agent"
	item, err := h.svc.CreateItem(ctx, store.CreateItemParams{
		ProjectID:          projectID,
		Type:               optStr(in.Type),
		Title:              in.Title,
		Body:               optStr(in.Body),
		Priority:           optStr(in.Priority),
		Status:             optStr(in.Status),
		Source:             &source,
		Tags:               in.Tags,
		Metadata:           metaJSON(in.Metadata),
		IdempotencyKey:     optStr(in.IdempotencyKey),
		AcceptanceCriteria: criteriaJSON(in.AcceptanceCriteria),
	}, auth.Actor(ctx))
	if err != nil {
		return dto.Item{}, err
	}
	return dto.ToItem(item), nil
}

func (h *handlers) createItems(ctx context.Context, _ *mcpsdk.CallToolRequest, in createItemsIn) (createItemsOut, error) {
	if len(in.Items) == 0 {
		return createItemsOut{}, fmt.Errorf("items is required")
	}
	params := make([]store.CreateItemParams, 0, len(in.Items))
	source := "agent"
	for _, it := range in.Items {
		if err := service.ValidateItemFields(it.Type, it.Status, it.Priority); err != nil {
			return createItemsOut{}, err
		}
		projectID, err := h.resolveProject(ctx, it.Project)
		if err != nil {
			return createItemsOut{}, err
		}
		params = append(params, store.CreateItemParams{
			ProjectID:          projectID,
			Type:               optStr(it.Type),
			Title:              it.Title,
			Body:               optStr(it.Body),
			Priority:           optStr(it.Priority),
			Status:             optStr(it.Status),
			Source:             &source,
			Tags:               it.Tags,
			Metadata:           metaJSON(it.Metadata),
			IdempotencyKey:     optStr(it.IdempotencyKey),
			AcceptanceCriteria: criteriaJSON(it.AcceptanceCriteria),
		})
	}
	created, err := h.svc.BulkCreateItems(ctx, params, auth.Actor(ctx))
	if err != nil {
		return createItemsOut{Items: dto.ToItems(created)}, err
	}
	return createItemsOut{Items: dto.ToItems(created)}, nil
}

func (h *handlers) updateItem(ctx context.Context, _ *mcpsdk.CallToolRequest, in updateItemIn) (dto.Item, error) {
	if err := service.ValidateItemFields(in.Type, in.Status, in.Priority); err != nil {
		return dto.Item{}, err
	}
	id, err := h.resolveItemRef(ctx, in.ID)
	if err != nil {
		return dto.Item{}, err
	}
	var expectedVersion *int32
	if in.ExpectedVersion != nil {
		v := int32(*in.ExpectedVersion)
		expectedVersion = &v
	}
	item, err := h.svc.UpdateItem(ctx, store.UpdateItemParams{
		ID:       id,
		Title:    optStr(in.Title),
		Type:     optStr(in.Type),
		Status:   optStr(in.Status),
		Priority: optStr(in.Priority),
		// Body/assignee pass through as-is (matching REST PATCH): an explicit
		// empty string clears the field, absent leaves it unchanged.
		Assignee:           in.Assignee,
		Body:               in.Body,
		Tags:               in.Tags,
		Position:           in.Position,
		Metadata:           metaJSON(in.Metadata),
		AcceptanceCriteria: criteriaJSON(in.AcceptanceCriteria),
		ExpectedVersion:    expectedVersion,
	}, auth.Actor(ctx))
	if err != nil {
		return dto.Item{}, err
	}
	return dto.ToItem(item), nil
}

func (h *handlers) logActivity(ctx context.Context, _ *mcpsdk.CallToolRequest, in logActivityIn) (dto.Activity, error) {
	if err := service.ValidateActivityKind(&in.Kind); err != nil {
		return dto.Activity{}, err
	}
	if err := service.ValidateConfidence(in.Confidence); err != nil {
		return dto.Activity{}, err
	}
	projectID, err := h.resolveProject(ctx, in.Project)
	if err != nil {
		return dto.Activity{}, err
	}
	var itemID pgtype.UUID
	if in.Item != nil && *in.Item != "" {
		id, err := h.resolveItemRef(ctx, *in.Item)
		if err != nil {
			return dto.Activity{}, err
		}
		itemID = pgtype.UUID{Bytes: id, Valid: true}
	}
	actor := auth.Actor(ctx)
	row, err := h.svc.LogActivity(ctx, store.CreateActivityParams{
		ProjectID:  projectID,
		ItemID:     itemID,
		Kind:       ptr(in.Kind),
		Actor:      &actor,
		Body:       ptr(in.Body),
		Confidence: optStr(in.Confidence),
		Metadata:   metaJSON(in.Metadata),
	})
	if err != nil {
		return dto.Activity{}, err
	}
	return dto.ToActivity(row), nil
}

func (h *handlers) updateProjectSummary(ctx context.Context, _ *mcpsdk.CallToolRequest, in updateSummaryIn) (dto.Project, error) {
	p, err := h.st.UpdateProjectSummary(ctx, store.UpdateProjectSummaryParams{Slug: in.Slug, Summary: in.Summary})
	if err != nil {
		return dto.Project{}, err
	}
	return dto.ToProject(p), nil
}

func (h *handlers) createProject(ctx context.Context, _ *mcpsdk.CallToolRequest, in createProjectIn) (dto.Project, error) {
	if in.Slug == "" || in.Name == "" {
		return dto.Project{}, fmt.Errorf("slug and name are required")
	}
	if err := service.ValidateProjectStatus(in.Status); err != nil {
		return dto.Project{}, err
	}
	p, err := h.st.CreateProject(ctx, store.CreateProjectParams{
		Slug:         in.Slug,
		Name:         in.Name,
		Status:       optStr(in.Status),
		Summary:      optStr(in.Summary),
		Instructions: optStr(in.Instructions),
		Aliases:      in.Aliases,
		RepoUrl:      optStr(in.RepoURL),
		SiteUrl:      optStr(in.SiteURL),
	})
	if err != nil {
		return dto.Project{}, err
	}
	return dto.ToProject(p), nil
}

func (h *handlers) resolveProjectTool(ctx context.Context, _ *mcpsdk.CallToolRequest, in resolveProjectIn) (dto.Project, error) {
	p, err := h.svc.ResolveProject(ctx, in.Path)
	if err != nil {
		return dto.Project{}, err
	}
	return dto.ToProject(p), nil
}

func (h *handlers) completeItem(ctx context.Context, _ *mcpsdk.CallToolRequest, in completeItemIn) (dto.Item, error) {
	id, err := h.resolveItemRef(ctx, in.Item)
	if err != nil {
		return dto.Item{}, err
	}
	var summary string
	if in.Summary != nil {
		summary = *in.Summary
	}
	item, err := h.svc.CompleteItem(ctx, id, in.Why, summary, auth.Actor(ctx))
	if err != nil {
		return dto.Item{}, err
	}
	return dto.ToItem(item), nil
}

func (h *handlers) setProjectInstructions(ctx context.Context, _ *mcpsdk.CallToolRequest, in setInstructionsIn) (dto.Project, error) {
	p, err := h.st.SetProjectInstructions(ctx, store.SetProjectInstructionsParams{Slug: in.Slug, Instructions: in.Instructions})
	if err != nil {
		return dto.Project{}, err
	}
	return dto.ToProject(p), nil
}

func (h *handlers) linkItems(ctx context.Context, _ *mcpsdk.CallToolRequest, in linkItemsIn) (linkOut, error) {
	if err := service.ValidateLinkKind(&in.Kind); err != nil {
		return linkOut{}, err
	}
	from, err := h.resolveItemRef(ctx, in.From)
	if err != nil {
		return linkOut{}, err
	}
	to, err := h.resolveItemRef(ctx, in.To)
	if err != nil {
		return linkOut{}, err
	}
	link, err := h.svc.LinkItems(ctx, from, to, in.Kind)
	if err != nil {
		return linkOut{}, err
	}
	return linkOut{Link: dto.ToItemLink(link)}, nil
}

func (h *handlers) unlinkItems(ctx context.Context, _ *mcpsdk.CallToolRequest, in unlinkItemsIn) (okOut, error) {
	id, err := uuid.Parse(in.ID)
	if err != nil {
		return okOut{}, fmt.Errorf("invalid id: %w", err)
	}
	if err := h.svc.DeleteLink(ctx, id); err != nil {
		return okOut{}, err
	}
	return okOut{OK: true}, nil
}

func (h *handlers) addItemRef(ctx context.Context, _ *mcpsdk.CallToolRequest, in addRefIn) (refOut, error) {
	if err := service.ValidateRefKind(in.Kind); err != nil {
		return refOut{}, err
	}
	id, err := h.resolveItemRef(ctx, in.Item)
	if err != nil {
		return refOut{}, err
	}
	var kind, label string
	if in.Kind != nil {
		kind = *in.Kind
	}
	if in.Label != nil {
		label = *in.Label
	}
	ref, err := h.svc.AddItemRef(ctx, id, kind, in.Ref, label)
	if err != nil {
		return refOut{}, err
	}
	return refOut{Ref: dto.ToItemRef(ref)}, nil
}

func (h *handlers) listItemRefs(ctx context.Context, _ *mcpsdk.CallToolRequest, in listRefsIn) (refsOut, error) {
	id, err := h.resolveItemRef(ctx, in.Item)
	if err != nil {
		return refsOut{}, err
	}
	refs, err := h.svc.ListItemRefs(ctx, id)
	if err != nil {
		return refsOut{}, err
	}
	return refsOut{Refs: dto.ToItemRefs(refs)}, nil
}

func (h *handlers) usageReport(ctx context.Context, _ *mcpsdk.CallToolRequest, in usageReportIn) (dto.UsageReport, error) {
	days := 7
	if in.Days != nil {
		days = *in.Days
	}
	rep, err := h.svc.UsageReport(ctx, days, h.toolNames)
	if err != nil {
		return dto.UsageReport{}, err
	}
	return rep, nil
}
