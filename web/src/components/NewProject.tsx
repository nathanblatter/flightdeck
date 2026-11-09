import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
  api,
  PROJECT_DOMAINS,
  type Project,
  type ProjectDomain,
  type ProjectSensitivity,
} from "../api";
import { projectTreeOrder } from "../lib";

const slugify = (s: string) =>
  s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");

export function NewProject({
  projects,
  onDone,
}: {
  projects: Project[];
  onDone: () => void;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugEdited, setSlugEdited] = useState(false);
  const [summary, setSummary] = useState("");
  const [parent, setParent] = useState("");
  const [domain, setDomain] = useState<ProjectDomain>("work");
  const [sensitivity, setSensitivity] = useState<ProjectSensitivity>("normal");

  const create = useMutation({
    mutationFn: () =>
      api.createProject({
        slug: slug.trim(),
        name: name.trim(),
        summary: summary.trim() || undefined,
        parent: parent || undefined,
        domain,
        sensitivity,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["projects"] });
      onDone();
    },
  });

  return (
    <form
      className="quick-add"
      onSubmit={(e) => {
        e.preventDefault();
        if (name.trim() && slug.trim()) create.mutate();
      }}
    >
      <input
        autoFocus
        placeholder="Project name…"
        value={name}
        onChange={(e) => {
          setName(e.target.value);
          if (!slugEdited) setSlug(slugify(e.target.value));
        }}
      />
      <input
        placeholder="slug"
        value={slug}
        onChange={(e) => {
          setSlugEdited(true);
          setSlug(slugify(e.target.value));
        }}
      />
      <input
        placeholder="summary (optional)"
        value={summary}
        onChange={(e) => setSummary(e.target.value)}
      />
      <select value={parent} onChange={(e) => setParent(e.target.value)}>
        <option value="">No parent</option>
        {projectTreeOrder(projects).map(({ project: p, depth }) => (
          <option key={p.slug} value={p.slug}>
            {" ".repeat(depth)}
            {depth > 0 && "└ "}
            {p.name}
          </option>
        ))}
      </select>
      <select
        value={domain}
        title="Domain"
        onChange={(e) => setDomain(e.target.value as ProjectDomain)}
      >
        {PROJECT_DOMAINS.map((d) => (
          <option key={d} value={d}>
            {d}
          </option>
        ))}
      </select>
      <select
        value={sensitivity}
        title="Sensitivity"
        onChange={(e) => setSensitivity(e.target.value as ProjectSensitivity)}
      >
        <option value="normal">normal</option>
        <option value="private">🔒 private</option>
      </select>
      <button
        className="btn primary"
        disabled={!name.trim() || !slug.trim() || create.isPending}
      >
        {create.isPending ? "Creating…" : "Create"}
      </button>
      <button type="button" className="btn" onClick={onDone}>
        Cancel
      </button>
      {create.isError && (
        <span className="muted sm">{(create.error as Error).message}</span>
      )}
    </form>
  );
}
