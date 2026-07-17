import { useMemo, useRef } from "react";
import {
  DndContext,
  DragOverlay,
  PointerSensor,
  useSensor,
  useSensors,
  useDroppable,
  type DragEndEvent,
  type DragStartEvent,
} from "@dnd-kit/core";
import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { Item, ItemStatus, Project } from "../api";
import { api } from "../api";
import { COLUMNS, PRIORITY_RANK, TYPE_GLYPH, PRIORITY_COLOR } from "../lib";
import { Card } from "./Card";

function Column({
  id,
  label,
  items,
  projects,
  onOpenProject,
  onOpenItem,
}: {
  id: ItemStatus;
  label: string;
  items: Item[];
  projects: Map<string, Project>;
  onOpenProject: (slug: string) => void;
  onOpenItem: (item: Item) => void;
}) {
  const { setNodeRef, isOver } = useDroppable({ id });
  return (
    <div className={`column${isOver ? " over" : ""}`} ref={setNodeRef}>
      <div className="column-head">
        <span>{label}</span>
        <span className="count">{items.length}</span>
      </div>
      <div className="column-body">
        {items.map((it) => (
          <Card
            key={it.id}
            item={it}
            project={projects.get(it.project_id)}
            onOpenProject={onOpenProject}
            onOpenItem={onOpenItem}
          />
        ))}
      </div>
    </div>
  );
}

export function Board({
  items,
  projects,
  onOpenProject,
  onOpenItem,
}: {
  items: Item[];
  projects: Map<string, Project>;
  onOpenProject: (slug: string) => void;
  onOpenItem: (item: Item) => void;
}) {
  const qc = useQueryClient();
  const [activeId, setActiveId] = useState<string | null>(null);
  // A real drag ends with pointerup on the card, which the browser follows
  // with a click — swallow that one so dropping a card doesn't open it.
  const justDragged = useRef(false);
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
  );

  const move = useMutation({
    mutationFn: ({ id, status }: { id: string; status: ItemStatus }) =>
      api.patchItem(id, { status }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["items"] });
      qc.invalidateQueries({ queryKey: ["activity"] });
    },
  });

  const byColumn = useMemo(() => {
    const map = new Map<ItemStatus, Item[]>();
    for (const c of COLUMNS) map.set(c.id, []);
    for (const it of items) {
      const bucket = map.get(it.status as ItemStatus);
      if (bucket) bucket.push(it);
    }
    for (const list of map.values())
      list.sort(
        (a, b) =>
          PRIORITY_RANK[a.priority] - PRIORITY_RANK[b.priority] ||
          a.position - b.position,
      );
    return map;
  }, [items]);

  const activeItem = items.find((i) => i.id === activeId);

  function onDragStart(e: DragStartEvent) {
    setActiveId(String(e.active.id));
  }
  function onDragEnd(e: DragEndEvent) {
    setActiveId(null);
    justDragged.current = true;
    setTimeout(() => {
      justDragged.current = false;
    }, 0);
    const overId = e.over?.id;
    if (!overId) return;
    const item = items.find((i) => i.id === e.active.id);
    if (item && item.status !== overId) {
      move.mutate({ id: item.id, status: overId as ItemStatus });
    }
  }

  return (
    <DndContext
      sensors={sensors}
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
    >
      <div className="board">
        {COLUMNS.map((c) => (
          <Column
            key={c.id}
            id={c.id}
            label={c.label}
            items={byColumn.get(c.id) ?? []}
            projects={projects}
            onOpenProject={onOpenProject}
            onOpenItem={(it) => {
              if (!justDragged.current) onOpenItem(it);
            }}
          />
        ))}
      </div>
      <DragOverlay>
        {activeItem ? (
          <div className="card dragging">
            <div className="card-top">
              <span className="glyph">{TYPE_GLYPH[activeItem.type] ?? "◆"}</span>
              <span className="card-title">{activeItem.title}</span>
              <span
                className="prio-dot"
                style={{ background: PRIORITY_COLOR[activeItem.priority] }}
              />
            </div>
          </div>
        ) : null}
      </DragOverlay>
    </DndContext>
  );
}
