import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, type Project } from "../api";
import { projectColor, projectTreeOrder, relativeTime, TYPE_GLYPH } from "../lib";

export function ProjectDrawer({
  slug,
  projects,
  onOpenProject,
  onClose,
}: {
  slug: string;
  projects: Project[];
  onOpenProject: (slug: string) => void;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const { data, isLoading } = useQuery({
    queryKey: ["context", slug],
    queryFn: () => api.projectContext(slug),
  });

  const [summary, setSummary] = useState("");
  const [dirty, setDirty] = useState(false);
  useEffect(() => {
    if (data) {
      setSummary(data.project.summary);
      setDirty(false);
    }
  }, [data]);

  const save = useMutation({
    mutationFn: () => api.patchProject(slug, { summary }),
    onSuccess: () => {
      setDirty(false);
      qc.invalidateQueries({ queryKey: ["context", slug] });
      qc.invalidateQueries({ queryKey: ["projects"] });
    },
  });

  // Deletion is two deliberate steps. Archiving is reversible and hides the
  // project from orient reads and pickers; purging is the hard delete and is
  // only offered once archived, behind a type-the-slug confirmation.
  const [confirmSlug, setConfirmSlug] = useState("");
  const archive = useMutation({
    mutationFn: (status: "archived" | "active") => api.patchProject(slug, { status }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["context"] });
      qc.invalidateQueries({ queryKey: ["projects"] });
    },
  });
  const purge = useMutation({
    mutationFn: () => api.deleteProject(slug),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["context"] });
      qc.invalidateQueries({ queryKey: ["projects"] });
      onClose();
    },
  });

  // "" clears the parent (back to root); the server rejects cycles with a 400.
  const setParent = useMutation({
    mutationFn: (parent: string) => api.patchProject(slug, { parent }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["context"] });
      qc.invalidateQueries({ queryKey: ["projects"] });
    },
  });

  // A project can't be its own parent, and nesting under a descendant would
  // cycle — filter both out so the picker only offers legal parents.
  const descendants = new Set<string>([slug]);
  let grew = true;
  while (grew) {
    grew = false;
    for (const p of projects) {
      if (p.parent && descendants.has(p.parent) && !descendants.has(p.slug)) {
        descendants.add(p.slug);
        grew = true;
      }
    }
  }
  const parentChoices = projectTreeOrder(
    projects.filter((p) => !descendants.has(p.slug)),
  );

  return (
    <div className="drawer-backdrop" onClick={onClose}>
      <aside className="drawer" onClick={(e) => e.stopPropagation()}>
        {isLoading || !data ? (
          <div className="muted pad">Loading…</div>
        ) : (
          <>
            <header className="drawer-head">
              {data.project.parent && (
                <button
                  className="project-chip"
                  style={{ background: projectColor(data.project.parent) }}
                  title="Open parent project"
                  onClick={() => onOpenProject(data.project.parent!)}
                >
                  {data.project.parent} ›
                </button>
              )}
              <span
                className="project-chip"
                style={{ background: projectColor(data.project.slug) }}
              >
                {data.project.slug}
              </span>
              <h2>{data.project.name}</h2>
              <button className="icon-btn" onClick={onClose}>
                ✕
              </button>
            </header>

            <section>
              <label className="field-label">Parent project</label>
              <select
                value={data.project.parent ?? ""}
                disabled={setParent.isPending}
                onChange={(e) => setParent.mutate(e.target.value)}
              >
                <option value="">None (root)</option>
                {parentChoices.map(({ project: p, depth }) => (
                  <option key={p.slug} value={p.slug}>
                    {" ".repeat(depth)}
                    {depth > 0 && "└ "}
                    {p.name}
                  </option>
                ))}
              </select>
              {setParent.isError && (
                <span className="muted sm">
                  {(setParent.error as Error).message}
                </span>
              )}
            </section>

            {data.children && data.children.length > 0 && (
              <section>
                <h3 className="section-title">
                  Sub-projects <span className="muted">({data.children.length})</span>
                </h3>
                <div className="chip-row">
                  {data.children.map((c) => (
                    <button
                      key={c.slug}
                      className="project-chip"
                      style={{ background: projectColor(c.slug) }}
                      title={c.summary || c.name}
                      onClick={() => onOpenProject(c.slug)}
                    >
                      {c.name}
                    </button>
                  ))}
                </div>
              </section>
            )}

            <section>
              <label className="field-label">
                Current state
                <span className="muted sm">
                  {" "}
                  · updated {relativeTime(data.summary_updated_at)}
                  {data.activities_since_summary >= 10 &&
                    ` · ${data.activities_since_summary} activities since — may be stale`}
                </span>
              </label>
              <textarea
                className="summary-edit"
                value={summary}
                onChange={(e) => {
                  setSummary(e.target.value);
                  setDirty(true);
                }}
              />
              <div className="drawer-actions">
                <button
                  className="btn primary"
                  disabled={!dirty || save.isPending}
                  onClick={() => save.mutate()}
                >
                  {save.isPending ? "Saving…" : "Save summary"}
                </button>
              </div>
            </section>

            {data.nudges && data.nudges.length > 0 && (
              <section>
                <h3 className="section-title">Nudges</h3>
                <ul className="drawer-list">
                  {data.nudges.map((n, i) => (
                    <li key={i} className="nudge">
                      ⚠ {n}
                    </li>
                  ))}
                </ul>
              </section>
            )}

            <section>
              <h3 className="section-title">
                Open items <span className="muted">({data.open_items.length})</span>
              </h3>
              <ul className="drawer-list">
                {data.open_items.map((it) => (
                  <li key={it.id}>
                    <span className="glyph">{TYPE_GLYPH[it.type] ?? "◆"}</span>
                    <span className="mono muted nowrap">{it.ref}</span>
                    <span className="ellipsis">{it.title}</span>
                    <span className="muted nowrap">{it.status}</span>
                  </li>
                ))}
                {data.open_items.length === 0 && (
                  <li className="muted">Nothing open 🎉</li>
                )}
              </ul>
            </section>

            <section>
              <h3 className="section-title">Recent activity</h3>
              <ul className="drawer-list">
                {data.recent_activity.map((a) => (
                  <li key={a.id} className="col">
                    <span>{a.body}</span>
                    <span className="muted sm">
                      {a.actor} · {relativeTime(a.created_at)}
                    </span>
                  </li>
                ))}
                {data.recent_activity.length === 0 && (
                  <li className="muted">No activity logged.</li>
                )}
              </ul>
            </section>

            <section className="danger-zone">
              <h3 className="section-title">Danger zone</h3>
              {data.project.status !== "archived" ? (
                <>
                  <p className="muted sm">
                    Archiving hides this project from orient reads and pickers.
                    Nothing is deleted and you can restore it at any time.
                  </p>
                  <div className="drawer-actions">
                    <button
                      className="btn"
                      disabled={archive.isPending}
                      onClick={() => archive.mutate("archived")}
                    >
                      {archive.isPending ? "Archiving…" : "Archive project"}
                    </button>
                  </div>
                </>
              ) : (
                <>
                  <p className="muted sm">
                    This project is archived. Restore it, or permanently delete
                    it and its {data.counts ? Object.values(data.counts).reduce((a, b) => a + b, 0) : 0} item(s)
                    plus all activity history. Deleting cannot be undone.
                  </p>
                  <div className="drawer-actions">
                    <button
                      className="btn"
                      disabled={archive.isPending}
                      onClick={() => archive.mutate("active")}
                    >
                      {archive.isPending ? "Restoring…" : "Restore to active"}
                    </button>
                  </div>
                  <label className="sm">
                    Type <code>{slug}</code> to confirm permanent deletion
                    <input
                      value={confirmSlug}
                      onChange={(e) => setConfirmSlug(e.target.value)}
                      placeholder={slug}
                    />
                  </label>
                  <div className="drawer-actions">
                    <button
                      className="btn danger"
                      disabled={confirmSlug !== slug || purge.isPending}
                      onClick={() => purge.mutate()}
                    >
                      {purge.isPending ? "Deleting…" : "Delete permanently"}
                    </button>
                  </div>
                  {purge.isError && (
                    <p className="sm error">{(purge.error as Error).message}</p>
                  )}
                </>
              )}
            </section>
          </>
        )}
      </aside>
    </div>
  );
}
