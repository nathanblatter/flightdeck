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
