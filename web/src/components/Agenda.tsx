import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, type AgendaEntry, type Item, type Project } from "../api";
import { PRIORITY_COLOR, TYPE_GLYPH, formatDue, projectColor } from "../lib";

const HORIZONS = [7, 14, 30, 90];

// AgendaView lists dated open items across active projects: overdue first,
// then what is due inside the chosen horizon. It is the calendar view of the
// board — the same items, ordered by when they expire instead of by column.
export function AgendaView({
  projects,
  projectFilter,
  onOpenProject,
  onOpenItem,
}: {
  projects: Project[];
  projectFilter?: string;
  onOpenProject: (slug: string) => void;
  onOpenItem: (item: Item) => void;
}) {
  const [days, setDays] = useState(14);
  const agendaQ = useQuery({
    queryKey: ["agenda", days],
    queryFn: () => api.agenda(days),
  });
  // Cards open through the same item modal as the board, which wants a full
  // Item; fetch it on click rather than widening the agenda payload.
  const open = async (e: AgendaEntry) => {
    const items = await api.items({ project: e.project });
    const it = items.find((i) => i.ref === e.ref);
    if (it) onOpenItem(it);
  };
  const bySlug = new Map(projects.map((p) => [p.slug, p]));
  const keep = (e: AgendaEntry) => !projectFilter || e.project === projectFilter;

  if (agendaQ.isLoading) return <div className="muted pad">Loading…</div>;
  if (agendaQ.isError) return <div className="muted pad">Could not load the agenda.</div>;
  const a = agendaQ.data!;
  const overdue = a.overdue.filter(keep);
  const upcoming = a.upcoming.filter(keep);

  return (
    <div className="agenda">
      <div className="agenda-head">
        <span className="muted">Dated open items across active projects</span>
        <div className="spacer" />
        <label className="muted">
          Horizon{" "}
          <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
            {HORIZONS.map((d) => (
              <option key={d} value={d}>
                {d} days
              </option>
            ))}
          </select>
        </label>
      </div>
      {a.nudges?.map((n) => (
        <div key={n} className="agenda-nudge">
          {n}
        </div>
      ))}
      <Section
        title="Overdue"
        entries={overdue}
        empty="Nothing overdue."
        bySlug={bySlug}
        onOpenProject={onOpenProject}
        onOpen={open}
      />
      <Section
        title={`Due in the next ${days} days`}
        entries={upcoming}
        empty="Nothing due in this window."
        bySlug={bySlug}
        onOpenProject={onOpenProject}
        onOpen={open}
      />
    </div>
  );
}

function Section({
  title,
  entries,
  empty,
  bySlug,
  onOpenProject,
  onOpen,
}: {
  title: string;
  entries: AgendaEntry[];
  empty: string;
  bySlug: Map<string, Project>;
  onOpenProject: (slug: string) => void;
  onOpen: (e: AgendaEntry) => void;
}) {
  return (
    <section className="agenda-section">
      <div className="column-head">
        <span>{title}</span>
        <span className="count">{entries.length}</span>
      </div>
      {entries.length === 0 ? (
        <div className="muted pad">{empty}</div>
      ) : (
        <ul className="agenda-list">
          {entries.map((e) => (
            <li key={e.ref} className="agenda-row" onClick={() => onOpen(e)}>
              <span className="glyph" title={e.type}>
                {TYPE_GLYPH[e.type] ?? "◆"}
              </span>
              <span className={`due-badge${e.overdue ? " overdue" : ""}`}>
                {formatDue(e.due_at)}
              </span>
              <span className="agenda-title">{e.title}</span>
              {e.expired && (
                <span className="blocked-badge" title="Past due with no activity since the due date">
                  expired?
                </span>
              )}
              <span
                className="prio-dot"
                title={e.priority}
                style={{ background: PRIORITY_COLOR[e.priority] }}
              />
              <span className="card-ref">{e.ref}</span>
              <button
                className="project-chip"
                style={{ background: projectColor(e.project) }}
                onClick={(ev) => {
                  ev.stopPropagation();
                  onOpenProject(e.project);
                }}
                title={`Open ${bySlug.get(e.project)?.name ?? e.project}`}
              >
                {e.project}
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
