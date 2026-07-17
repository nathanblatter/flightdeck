import { useQuery } from "@tanstack/react-query";
import type { Project } from "../api";
import { api } from "../api";
import { projectColor, relativeTime } from "../lib";

const KIND_LABEL: Record<string, string> = {
  decision: "Decision",
  progress: "Progress",
  status_change: "Status",
  comment: "Comment",
  created: "Created",
};

export function ActivityFeed({
  projectFilter,
  projects,
}: {
  projectFilter?: string;
  projects: Map<string, Project>;
}) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["activity", projectFilter],
    queryFn: () => api.activity({ project: projectFilter }),
  });

  if (isLoading) return <div className="muted pad">Loading activity…</div>;
  if (error) return <div className="muted pad">Failed to load activity.</div>;
  if (!data || data.length === 0)
    return <div className="muted pad">No activity yet.</div>;

  return (
    <div className="feed">
      {data.map((a) => {
        const project = projects.get(a.project_id);
        return (
          <div key={a.id} className="feed-row">
            <div className="feed-line">
              <span className={`kind kind-${a.kind}`}>
                {KIND_LABEL[a.kind] ?? a.kind}
              </span>
              {project && (
                <span
                  className="project-chip sm"
                  style={{ background: projectColor(project.slug) }}
                >
                  {project.slug}
                </span>
              )}
              <span className="feed-body">{a.body}</span>
            </div>
            <div className="feed-meta">
              <span>{a.actor || "unknown"}</span>
              <span>·</span>
              <span>{relativeTime(a.created_at)}</span>
            </div>
          </div>
        );
      })}
    </div>
  );
}
