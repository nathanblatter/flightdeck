import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { COLUMNS, PRIORITY_COLOR, PRIORITY_RANK, projectColor, relativeTime, updateNotice } from "./lib";

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
