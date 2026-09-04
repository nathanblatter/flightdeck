import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  api,
  getApiKey,
  setApiKey,
  clearApiKey,
  setupStatus,
  type Item,
  type Project,
  type SetupStatus,
} from "./api";
import { projectTreeOrder, updateNotice } from "./lib";
import { SetupWizard } from "./components/SetupWizard";
import { Board } from "./components/Board";
import { ActivityFeed } from "./components/ActivityFeed";
import { ProjectDrawer } from "./components/ProjectDrawer";
import { QuickAdd } from "./components/QuickAdd";
import { NewProject } from "./components/NewProject";
import { ItemLinksModal } from "./components/ItemLinks";

type View = "board" | "activity";

export function App() {
  const [hasKey, setHasKey] = useState(!!getApiKey());
  // Setup status is unauthenticated and decides whether to show the first-run
  // wizard instead of the key gate. It also carries the instance name.
  const setupQ = useQuery({
    queryKey: ["setup"],
    queryFn: setupStatus,
    staleTime: Infinity,
    retry: 1,
  });
  const instanceName = setupQ.data?.instance_name || "Flightdeck";
  useEffect(() => {
    document.title = instanceName;
  }, [instanceName]);

  if (setupQ.isLoading) return null;
  if (setupQ.data && !setupQ.data.setup_complete) {
    return (
      <SetupWizard
        onDone={() => {
          setHasKey(!!getApiKey());
          void setupQ.refetch();
        }}
      />
    );
  }
  if (!hasKey) return <KeyGate onSet={() => setHasKey(true)} />;
  return (
    <Dashboard
      instanceName={instanceName}
      setup={setupQ.data}
      onSignOut={() => setHasKey(false)}
    />
  );
}

function KeyGate({ onSet }: { onSet: () => void }) {
  const [val, setVal] = useState("");
  return (
    <div className="gate">
      <div className="gate-card">
        <h1>🛩 Flightdeck</h1>
        <p className="muted">Enter your API key to continue.</p>
        <input
          type="password"
          placeholder="fd_…"
          value={val}
          onChange={(e) => setVal(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && val.trim()) {
              setApiKey(val);
              onSet();
            }
          }}
        />
        <button
          className="btn primary"
          disabled={!val.trim()}
          onClick={() => {
            setApiKey(val);
            onSet();
          }}
        >
          Continue
        </button>
      </div>
    </div>
  );
}

// MIN_SEARCH_LEN mirrors the server's FLIGHTDECK_MIN_SEMANTIC_QUERY_LEN (3):
// shorter queries are keystroke fragments, so the UI shows the plain list
// instead of routing to /search.
const MIN_SEARCH_LEN = 3;

// useLiveUpdates subscribes to the server's SSE stream and invalidates the
// affected React Query caches on each mutation event, so the board reflects
// changes live instead of waiting for a poll. EventSource can't set headers, so
// the API key rides in the query string (the server logs only the path). It
// auto-reconnects on drop (the server sends a retry hint); on unmount we close
// it. Query invalidation is coalesced by React Query, so a burst of events
// triggers at most one refetch per key.
function useLiveUpdates() {
  const qc = useQueryClient();
  useEffect(() => {
    const key = getApiKey();
    if (!key) return;
    const es = new EventSource(`/api/stream?api_key=${encodeURIComponent(key)}`);
    es.onmessage = () => {
      for (const k of ["items", "projects", "activity", "context", "links"]) {
        qc.invalidateQueries({ queryKey: [k] });
      }
    };
    return () => es.close();
  }, [qc]);
}

// useDebounced returns `value` after it has stayed unchanged for `ms`.
function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return debounced;
}

function Dashboard({
  instanceName,
  setup,
  onSignOut,
}: {
  instanceName: string;
  setup?: SetupStatus;
  onSignOut: () => void;
}) {
  const [view, setView] = useState<View>("board");
  const [updateDismissed, setUpdateDismissed] = useState(false);
  const notice = updateNotice(setup);
  const [projectFilter, setProjectFilter] = useState("");
  const [typeFilter, setTypeFilter] = useState("");
  const [tagFilter, setTagFilter] = useState("");
  const [search, setSearch] = useState("");
  const [adding, setAdding] = useState(false);
  const [addingProject, setAddingProject] = useState(false);
  const [drawerSlug, setDrawerSlug] = useState<string | null>(null);
  const [linksItem, setLinksItem] = useState<Item | null>(null);

  // Live updates over SSE — pushes replace polling for freshness.
  useLiveUpdates();

  const projectsQ = useQuery({ queryKey: ["projects"], queryFn: api.projects });
  const projects: Project[] = projectsQ.data ?? [];
  const projectById = useMemo(() => {
    const m = new Map<string, Project>();
    for (const p of projects) m.set(p.id, p);
    return m;
  }, [projects]);

  // Debounce the search term: a non-empty query routes to /search, whose
  // semantic tier embeds the query via OpenAI when full-text finds nothing —
  // so we wait for a typing pause rather than firing a call per keystroke.
  // Below MIN_SEARCH_LEN we don't search at all: a 1–2 char fragment ("j", "jo")
  // can't carry meaning and just burns a round-trip on a zero-result path. This
  // mirrors the server's FLIGHTDECK_MIN_SEMANTIC_QUERY_LEN embedding guard.
  const debouncedSearch = useDebounced(search.trim(), 300);
  const searching = debouncedSearch.length >= MIN_SEARCH_LEN;

  const itemsQ = useQuery({
    queryKey: ["items", projectFilter, typeFilter, tagFilter, debouncedSearch],
    queryFn: () =>
      searching
        ? api
            .search({
              q: debouncedSearch,
              project: projectFilter || undefined,
              type: typeFilter || undefined,
            })
            .then((r) => r.items)
        : api.items({
            project: projectFilter || undefined,
            type: typeFilter || undefined,
            tag: tagFilter || undefined,
          }),
  });
  // Tag filter isn't a /search param, so apply it client-side over results.
  const items: Item[] = useMemo(() => {
    const data = itemsQ.data ?? [];
    if (searching && tagFilter.trim()) {
      return data.filter((i) => i.tags?.includes(tagFilter.trim()));
    }
    return data;
  }, [itemsQ.data, searching, tagFilter]);

  const authStatus = (projectsQ.error as Error & { status?: number })?.status;
  const authError = authStatus === 401 || authStatus === 403;
  // Sign out in an effect, not during render (render must stay side-effect-free).
  useEffect(() => {
    if (authError) {
      clearApiKey();
      onSignOut();
    }
  }, [authError, onSignOut]);

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">🛩 {instanceName}</div>
        <nav className="tabs">
          <button
            className={view === "board" ? "active" : ""}
            onClick={() => setView("board")}
          >
            Board
          </button>
          <button
            className={view === "activity" ? "active" : ""}
            onClick={() => setView("activity")}
          >
            Activity
          </button>
        </nav>
        <div className="spacer" />
        <button className="btn primary" onClick={() => setAdding((a) => !a)}>
          + Quick add
        </button>
        <button className="btn" onClick={() => setAddingProject((a) => !a)}>
          + Project
        </button>
        <button
          className="btn ghost"
          title="Sign out"
          onClick={() => {
            clearApiKey();
            onSignOut();
          }}
        >
          ⎋
        </button>
      </header>

      {notice && !updateDismissed && (
        <div className="update-banner">
          <span>
            {notice}{" "}
            {setup?.update_url && (
              <a href={setup.update_url} target="_blank" rel="noreferrer">
                release notes
              </a>
            )}
          </span>
          <button
            className="btn ghost"
            title="Dismiss"
            onClick={() => setUpdateDismissed(true)}
          >
            ✕
          </button>
        </div>
      )}

      <div className="filterbar">
        <select
          value={projectFilter}
          onChange={(e) => setProjectFilter(e.target.value)}
        >
          <option value="">All projects</option>
          {projectTreeOrder(projects).map(({ project: p, depth }) => (
            <option key={p.slug} value={p.slug}>
              {" ".repeat(depth)}
              {depth > 0 && "└ "}
              {p.name}
            </option>
          ))}
        </select>
        <select value={typeFilter} onChange={(e) => setTypeFilter(e.target.value)}>
          <option value="">All types</option>
          <option value="task">tasks</option>
          <option value="bug">bugs</option>
          <option value="idea">ideas</option>
          <option value="note">notes</option>
        </select>
        <input
          placeholder="tag"
          value={tagFilter}
          onChange={(e) => setTagFilter(e.target.value)}
          className="tag-input"
        />
        <input
          placeholder="Search (semantic)…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="search-input"
        />
        {itemsQ.isFetching && (
          <span className="muted sm">{searching ? "searching…" : "syncing…"}</span>
        )}
      </div>

      {adding && (
        <QuickAdd
          projects={projects}
          defaultProject={projectFilter}
          onDone={() => setAdding(false)}
        />
      )}
      {addingProject && (
        <NewProject projects={projects} onDone={() => setAddingProject(false)} />
      )}

      <main className="content">
        {projectsQ.isLoading ? (
          <div className="muted pad">Loading…</div>
        ) : projects.length === 0 ? (
          <div className="muted pad">
            No projects yet. Use “+ Project” above to create one.
          </div>
        ) : view === "board" ? (
          <Board
            items={items}
            projects={projectById}
            onOpenProject={setDrawerSlug}
            onOpenItem={setLinksItem}
          />
        ) : (
          <ActivityFeed
            projectFilter={projectFilter || undefined}
            projects={projectById}
          />
        )}
      </main>

      {drawerSlug && (
        <ProjectDrawer
          slug={drawerSlug}
          projects={projects}
          onOpenProject={setDrawerSlug}
          onClose={() => setDrawerSlug(null)}
        />
      )}
      {linksItem && (
        <ItemLinksModal item={linksItem} onClose={() => setLinksItem(null)} />
      )}
    </div>
  );
}
