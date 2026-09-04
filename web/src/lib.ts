import type { ItemStatus, Priority } from "./api";

// Kanban columns in board order. `wontfix` is intentionally omitted from the
// board (reachable via filters) to keep the wall of cards meaningful.
export const COLUMNS: { id: ItemStatus; label: string }[] = [
  { id: "backlog", label: "Backlog" },
  { id: "todo", label: "To do" },
  { id: "in_progress", label: "In progress" },
  { id: "blocked", label: "Blocked" },
  { id: "done", label: "Done" },
];

export const PRIORITY_RANK: Record<Priority, number> = {
  urgent: 0,
  high: 1,
  med: 2,
  low: 3,
};

export const PRIORITY_COLOR: Record<Priority, string> = {
  urgent: "#dc2626",
  high: "#ea580c",
  med: "#2563eb",
  low: "#6b7280",
};

export const TYPE_GLYPH: Record<string, string> = {
  task: "◆",
  bug: "🐞",
  idea: "💡",
  note: "✎",
};

// Deterministic, pleasant color per project slug (stable across reloads).
export function projectColor(slug: string): string {
  let h = 0;
  for (let i = 0; i < slug.length; i++) h = (h * 31 + slug.charCodeAt(i)) % 360;
  return `hsl(${h} 65% 45%)`;
}

export function relativeTime(iso: string): string {
  const then = new Date(iso).getTime();
  const diff = Date.now() - then;
  const s = Math.round(diff / 1000);
  if (s < 60) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d < 30) return `${d}d ago`;
  return new Date(iso).toLocaleDateString();
}

// projectTreeOrder flattens the project tree depth-first for pickers: roots in
// the given (server-ranked) order, each followed by its children, with depth
// for indentation. A project whose parent isn't in the list (filtered out or
// paused) renders as a root rather than disappearing; a stray cycle can't hang
// the walk because visited nodes are never re-entered.
export function projectTreeOrder<P extends { slug: string; parent?: string }>(
  projects: P[],
): { project: P; depth: number }[] {
  const bySlug = new Set(projects.map((p) => p.slug));
  const children = new Map<string, P[]>();
  const roots: P[] = [];
  for (const p of projects) {
    if (p.parent && bySlug.has(p.parent)) {
      const list = children.get(p.parent) ?? [];
      list.push(p);
      children.set(p.parent, list);
    } else {
      roots.push(p);
    }
  }
  const out: { project: P; depth: number }[] = [];
  const visited = new Set<string>();
  const walk = (p: P, depth: number) => {
    if (visited.has(p.slug)) return;
    visited.add(p.slug);
    out.push({ project: p, depth });
    for (const c of children.get(p.slug) ?? []) walk(c, depth + 1);
  };
  for (const r of roots) walk(r, 0);
  // Anything unvisited is inside an orphaned cycle — surface it flat.
  for (const p of projects) walk(p, 0);
  return out;
}

// updateNotice returns the banner line when the server reports a newer
// published release, else null. The server (internal/update) already did the
// semver comparison — an empty/absent latest_version means "current".
export function updateNotice(status?: {
  version: string;
  latest_version?: string;
}): string | null {
  if (!status?.latest_version) return null;
  return `Flightdeck ${status.latest_version} is available (running ${status.version}) — run \`flightdeck update\` on the host to upgrade.`;
}
