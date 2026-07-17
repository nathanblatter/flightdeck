// Typed client for the flightdeck HTTP API. The single-user API key lives in
// localStorage and is sent as X-API-Key on every request.

const KEY_STORAGE = "flightdeck_key";

export function getApiKey(): string {
  return localStorage.getItem(KEY_STORAGE) ?? "";
}
export function setApiKey(key: string) {
  localStorage.setItem(KEY_STORAGE, key.trim());
}
export function clearApiKey() {
  localStorage.removeItem(KEY_STORAGE);
}

export type ProjectStatus = "active" | "paused" | "done" | "archived";
export type ItemStatus =
  | "backlog"
  | "todo"
  | "in_progress"
  | "blocked"
  | "done"
  | "wontfix";
export type ItemType = "task" | "bug" | "idea" | "note";
export type Priority = "low" | "med" | "high" | "urgent";

export interface Project {
  id: string;
  slug: string;
  name: string;
  status: ProjectStatus;
  summary: string;
  repo_url?: string;
  site_url?: string;
  created_at: string;
  updated_at: string;
}

export interface Item {
  id: string;
  ref: string;
  project_id: string;
  type: ItemType;
  title: string;
  body: string;
  status: ItemStatus;
  priority: Priority;
  assignee?: string;
  position: number;
  source: string;
  external_ref?: string;
  tags: string[];
  metadata?: Record<string, unknown>;
  created_at: string;
  updated_at: string;
  closed_at?: string;
  version: number;
  // Set on list/context responses when an open item with a `blocks` link
  // points at this one; blocked_by carries the blocker titles.
  blocked?: boolean;
  blocked_by?: string[];
}

export type LinkKind = "blocks" | "relates_to" | "parent_of";

// ItemLink is a directed relationship: from --kind--> to.
export interface ItemLink {
  id: string;
  from_item_id: string;
  to_item_id: string;
  kind: LinkKind;
  created_at: string;
}

export interface ItemBrief {
  ref: string;
  title: string;
  status: ItemStatus;
  priority: Priority;
  type: ItemType;
}

export interface Activity {
  id: string;
  project_id: string;
  item_id?: string;
  kind:
    | "decision"
    | "progress"
    | "status_change"
    | "comment"
    | "created"
    | "rejected";
  actor: string;
  body: string;
  confidence?: string;
  metadata?: Record<string, unknown>;
  created_at: string;
}

export interface ProjectContext {
  project: Project;
  open_items: Item[];
  recent_activity: Activity[];
  counts?: Record<string, number>;
  ready_next?: ItemBrief[];
  nudges?: string[];
  rejected_approaches?: Activity[];
  // Trust signals: when the summary was last written and how much has happened
  // since (high count ⇒ treat it as stale).
  summary_updated_at: string;
  activities_since_summary: number;
}

export interface SearchResults {
  query: string;
  items: Item[];
  activity: Activity[];
}

export interface WebhookEvent {
  id: string;
  project_id?: string;
  event: string;
  attempts: number;
  next_attempt_at: string;
  delivered_at?: string;
  // Dead-lettered (attempts exhausted) — distinct from delivered.
  parked_at?: string;
  // Subscribers that already ACKed; retries skip these.
  delivered_hook_ids?: string[];
  last_error?: string;
  created_at: string;
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function req<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const res = await fetch(`/api${path}`, {
    method,
    headers: {
      "X-API-Key": getApiKey(),
      ...(body ? { "Content-Type": "application/json" } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const j = await res.json();
      if (j.error) msg = j.error;
    } catch {
      /* ignore */
    }
    throw new ApiError(res.status, msg);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

function qs(params: Record<string, string | undefined>): string {
  const sp = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) sp.set(k, v);
  const s = sp.toString();
  return s ? `?${s}` : "";
}

export interface ItemFilters {
  project?: string;
  status?: string;
  type?: string;
  tag?: string;
  q?: string;
}

export const api = {
  projects: () => req<Project[]>("GET", "/projects"),
  items: (f: ItemFilters = {}) =>
    req<Item[]>("GET", `/items${qs({ ...f })}`),
  // Cascading recall over items + activity: full-text → semantic (pgvector
  // cosine) → trigram. Unlike `items`, finds conceptually-related matches with
  // no shared keywords.
  search: (f: { q: string; project?: string; type?: string }) =>
    req<SearchResults>("GET", `/search${qs({ ...f })}`),
  createItem: (body: {
    project: string;
    title: string;
    type?: string;
    body?: string;
    priority?: string;
    tags?: string[];
  }) => req<Item>("POST", "/items", body),
  patchItem: (id: string, body: Partial<Pick<Item, "status" | "priority" | "assignee" | "body" | "title" | "tags" | "position">>) =>
    req<Item>("PATCH", `/items/${id}`, body),
  deleteItem: (id: string) => req<void>("DELETE", `/items/${id}`),
  itemLinks: (id: string) => req<ItemLink[]>("GET", `/items/${id}/links`),
  createLink: (body: { from: string; to: string; kind: LinkKind }) =>
    req<ItemLink>("POST", "/links", body),
  deleteLink: (id: string) => req<void>("DELETE", `/links/${id}`),
  activity: (f: { project?: string; kind?: string } = {}) =>
    req<Activity[]>("GET", `/activity${qs({ ...f })}`),
  projectContext: (slug: string) =>
    req<ProjectContext>("GET", `/context/${slug}`),
  webhookEvents: () => req<WebhookEvent[]>("GET", "/webhooks/events"),
  patchProject: (slug: string, body: Partial<Pick<Project, "name" | "status" | "summary" | "repo_url" | "site_url">>) =>
    req<Project>("PATCH", `/projects/${slug}`, body),
  createProject: (body: { slug: string; name: string; summary?: string }) =>
    req<Project>("POST", "/projects", body),
};
