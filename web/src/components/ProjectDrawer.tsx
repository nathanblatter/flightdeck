import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { projectColor, relativeTime, TYPE_GLYPH } from "../lib";

export function ProjectDrawer({
  slug,
  onClose,
}: {
  slug: string;
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

  return (
    <div className="drawer-backdrop" onClick={onClose}>
      <aside className="drawer" onClick={(e) => e.stopPropagation()}>
        {isLoading || !data ? (
          <div className="muted pad">Loading…</div>
        ) : (
          <>
            <header className="drawer-head">
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
              <label className="field-label">Current state</label>
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
          </>
        )}
      </aside>
    </div>
  );
}
