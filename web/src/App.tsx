import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  api,
  getApiKey,
  setApiKey,
  clearApiKey,
  type Item,
  type Project,
} from "./api";
import { Board } from "./components/Board";
import { ActivityFeed } from "./components/ActivityFeed";
import { ProjectDrawer } from "./components/ProjectDrawer";
import { QuickAdd } from "./components/QuickAdd";
import { ItemLinksModal } from "./components/ItemLinks";

type View = "board" | "activity";

export function App() {
  const [hasKey, setHasKey] = useState(!!getApiKey());
  if (!hasKey) return <KeyGate onSet={() => setHasKey(true)} />;
  return <Dashboard onSignOut={() => setHasKey(false)} />;
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

// useDebounced returns `value` after it has stayed unchanged for `ms`.
function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return debounced;
}

function Dashboard({ onSignOut }: { onSignOut: () => void }) {
  const [view, setView] = useState<View>("board");
  const [projectFilter, setProjectFilter] = useState("");
  const [typeFilter, setTypeFilter] = useState("");
  const [tagFilter, setTagFilter] = useState("");
  const [search, setSearch] = useState("");
  const [adding, setAdding] = useState(false);
  const [drawerSlug, setDrawerSlug] = useState<string | null>(null);
  const [linksItem, setLinksItem] = useState<Item | null>(null);

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
        <div className="brand">🛩 Flightdeck</div>
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

      <div className="filterbar">
        <select
          value={projectFilter}
          onChange={(e) => setProjectFilter(e.target.value)}
        >
          <option value="">All projects</option>
          {projects.map((p) => (
            <option key={p.slug} value={p.slug}>
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

      <main className="content">
        {projectsQ.isLoading ? (
          <div className="muted pad">Loading…</div>
        ) : projects.length === 0 ? (
          <div className="muted pad">
            No projects yet. Create one via the API or MCP to get started.
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
        <ProjectDrawer slug={drawerSlug} onClose={() => setDrawerSlug(null)} />
      )}
      {linksItem && (
        <ItemLinksModal item={linksItem} onClose={() => setLinksItem(null)} />
      )}
    </div>
  );
}
