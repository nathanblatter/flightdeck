import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { COLUMNS, PRIORITY_COLOR, PRIORITY_RANK, projectColor, projectTreeOrder, relativeTime, updateNotice } from "./lib";

describe("COLUMNS", () => {
  it("keeps board order and omits wontfix", () => {
    expect(COLUMNS.map((c) => c.id)).toEqual([
      "backlog",
      "todo",
      "in_progress",
      "blocked",
      "done",
    ]);
  });
});

describe("priority tables", () => {
  it("rank orders urgent first", () => {
    const ordered = Object.entries(PRIORITY_RANK).sort((a, b) => a[1] - b[1]);
    expect(ordered.map(([p]) => p)).toEqual(["urgent", "high", "med", "low"]);
  });

  it("every ranked priority has a color", () => {
    for (const p of Object.keys(PRIORITY_RANK)) {
      expect(PRIORITY_COLOR[p as keyof typeof PRIORITY_COLOR]).toMatch(/^#[0-9a-f]{6}$/);
    }
  });
});

describe("projectColor", () => {
  it("is deterministic per slug", () => {
    expect(projectColor("flightdeck")).toBe(projectColor("flightdeck"));
  });

  it("emits a valid hsl() with hue in [0, 360)", () => {
    for (const slug of ["flightdeck", "finforge", "natebot", "x"]) {
      const m = projectColor(slug).match(/^hsl\((\d+) 65% 45%\)$/);
      expect(m, slug).not.toBeNull();
      expect(Number(m![1])).toBeGreaterThanOrEqual(0);
      expect(Number(m![1])).toBeLessThan(360);
    }
  });
});

describe("relativeTime", () => {
  const now = new Date("2026-08-15T12:00:00Z");

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(now);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const at = (msAgo: number) => new Date(now.getTime() - msAgo).toISOString();

  it.each([
    ["under a minute", 30_000, "just now"],
    ["minutes", 5 * 60_000, "5m ago"],
    ["hours", 3 * 3_600_000, "3h ago"],
    ["days", 4 * 86_400_000, "4d ago"],
  ])("%s", (_name, msAgo, want) => {
    expect(relativeTime(at(msAgo))).toBe(want);
  });

  it("falls back to a locale date after ~30 days", () => {
    const old = at(45 * 86_400_000);
    expect(relativeTime(old)).toBe(new Date(old).toLocaleDateString());
  });
});

describe("projectTreeOrder", () => {
  const p = (slug: string, parent?: string) => ({ slug, parent });

  it("keeps flat lists in server order at depth 0", () => {
    const flat = [p("a"), p("b"), p("c")];
    expect(projectTreeOrder(flat)).toEqual(flat.map((project) => ({ project, depth: 0 })));
  });

  it("nests children under their parent depth-first", () => {
    const out = projectTreeOrder([p("root"), p("other"), p("mid", "root"), p("leaf", "mid")]);
    expect(out.map((e) => [e.project.slug, e.depth])).toEqual([
      ["root", 0],
      ["mid", 1],
      ["leaf", 2],
      ["other", 0],
    ]);
  });

  it("treats a child whose parent is filtered out as a root", () => {
    const out = projectTreeOrder([p("child", "hidden-parent")]);
    expect(out).toEqual([{ project: p("child", "hidden-parent"), depth: 0 }]);
  });

  it("surfaces every project even in a stray cycle", () => {
    const out = projectTreeOrder([p("a", "b"), p("b", "a")]);
    expect(out.map((e) => e.project.slug).sort()).toEqual(["a", "b"]);
  });
});

describe("updateNotice", () => {
  it("returns null when current or unknown", () => {
    expect(updateNotice(undefined)).toBeNull();
    expect(updateNotice({ version: "v0.1.0" })).toBeNull();
    expect(updateNotice({ version: "v0.1.0", latest_version: "" })).toBeNull();
  });

  it("names both versions and the update command", () => {
    const n = updateNotice({ version: "v0.1.0", latest_version: "v0.2.0" });
    expect(n).toContain("v0.2.0");
    expect(n).toContain("v0.1.0");
    expect(n).toContain("flightdeck update");
  });
});
