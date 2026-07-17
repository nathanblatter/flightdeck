import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { Project } from "../api";
import { api } from "../api";

export function QuickAdd({
  projects,
  defaultProject,
  onDone,
}: {
  projects: Project[];
  defaultProject?: string;
  onDone: () => void;
}) {
  const qc = useQueryClient();
  const [project, setProject] = useState(
    defaultProject || projects[0]?.slug || "",
  );
  const [title, setTitle] = useState("");
  const [type, setType] = useState("task");
  const [priority, setPriority] = useState("med");
  const [tags, setTags] = useState("");

  const create = useMutation({
    mutationFn: () =>
      api.createItem({
        project,
        title: title.trim(),
        type,
        priority,
        tags: tags
          .split(",")
          .map((t) => t.trim())
          .filter(Boolean),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["items"] });
      qc.invalidateQueries({ queryKey: ["activity"] });
      onDone();
    },
  });

  return (
    <form
      className="quick-add"
      onSubmit={(e) => {
        e.preventDefault();
        if (title.trim() && project) create.mutate();
      }}
    >
      <select value={project} onChange={(e) => setProject(e.target.value)}>
        {projects.map((p) => (
          <option key={p.slug} value={p.slug}>
            {p.name}
          </option>
        ))}
      </select>
      <input
        autoFocus
        placeholder="New item title…"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
      />
      <select value={type} onChange={(e) => setType(e.target.value)}>
        <option value="task">task</option>
        <option value="bug">bug</option>
        <option value="idea">idea</option>
        <option value="note">note</option>
      </select>
      <select value={priority} onChange={(e) => setPriority(e.target.value)}>
        <option value="low">low</option>
        <option value="med">med</option>
        <option value="high">high</option>
        <option value="urgent">urgent</option>
      </select>
      <input
        placeholder="tags (comma sep)"
        value={tags}
        onChange={(e) => setTags(e.target.value)}
      />
      <button className="btn primary" disabled={!title.trim() || create.isPending}>
        {create.isPending ? "Adding…" : "Add"}
      </button>
      <button type="button" className="btn" onClick={onDone}>
        Cancel
      </button>
    </form>
  );
}
