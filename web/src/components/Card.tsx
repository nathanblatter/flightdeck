import { useDraggable } from "@dnd-kit/core";
import type { Item, Project } from "../api";
import { PRIORITY_COLOR, TYPE_GLYPH, projectColor } from "../lib";

export function Card({
  item,
  project,
  onOpenProject,
  onOpenItem,
}: {
  item: Item;
  project?: Project;
  onOpenProject: (slug: string) => void;
  onOpenItem: (item: Item) => void;
}) {
  const { attributes, listeners, setNodeRef, transform, isDragging } =
    useDraggable({ id: item.id });

  const style = transform
    ? {
        transform: `translate3d(${transform.x}px, ${transform.y}px, 0)`,
        zIndex: 50,
      }
    : undefined;

  return (
    <div
      ref={setNodeRef}
      style={style}
      className={`card${isDragging ? " dragging" : ""}`}
      {...listeners}
      {...attributes}
      onClick={() => onOpenItem(item)}
    >
      <div className="card-top">
        <span className="glyph" title={item.type}>
          {TYPE_GLYPH[item.type] ?? "◆"}
        </span>
        <span className="card-title">{item.title}</span>
        <span
          className="prio-dot"
          title={item.priority}
          style={{ background: PRIORITY_COLOR[item.priority] }}
        />
      </div>
      <div className="card-meta">
        <span className="card-ref" title="item ref">{item.ref}</span>
        {project && (
          <button
            className="project-chip"
            style={{ background: projectColor(project.slug) }}
            onClick={(e) => {
              e.stopPropagation();
              onOpenProject(project.slug);
            }}
            title={`Open ${project.name}`}
          >
            {project.slug}
          </button>
        )}
        {item.blocked && (
          <span
            className="blocked-badge"
            title={`Blocked by: ${(item.blocked_by ?? []).join(", ")}`}
          >
            ⛔ blocked
          </span>
        )}
        {item.assignee && <span className="assignee">@{item.assignee}</span>}
        {item.tags.map((t) => (
          <span key={t} className="tag">
            #{t}
          </span>
        ))}
      </div>
    </div>
  );
}
