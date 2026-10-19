package dto

import (
	"strings"
	"testing"

	"flightdeck/internal/store"
)

func TestFirstSentence(t *testing.T) {
	cases := map[string]string{
		"One. Two. Three.":                 "One.",
		"No terminator here":               "No terminator here",
		"Line one\nline two":               "Line one",
		"v1.2 shipped. Next up: v2":        "v1.2 shipped.",
		"  padded?  then more":             "padded?",
		"Ends with period.":                "Ends with period.",
		"":                                 "",
		"Go + pgx/sqlc (one binary). More": "Go + pgx/sqlc (one binary).",
	}
	for in, want := range cases {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToProjectListBriefsCapsSummaryAndDropsInstructions(t *testing.T) {
	parent := "work"
	long := strings.Repeat("word ", 100) // 500 chars, no sentence end
	briefs := ToProjectListBriefs([]store.Project{{
		Slug: "a", Name: "A", Status: "active", ParentSlug: &parent, Aliases: []string{"alpha"},
		Summary: long, Instructions: "never shown",
	}, {
		Slug: "b", Name: "B", Status: "paused", Summary: "Short. Then a lot more text.",
	}})
	a, b := briefs[0], briefs[1]
	if a.Parent == nil || *a.Parent != "work" || len(a.Aliases) != 1 || a.Aliases[0] != "alpha" {
		t.Fatalf("parent/aliases lost: %+v", a)
	}
	if n := len([]rune(a.Summary)); n != briefSummaryMax+1 || !strings.HasSuffix(a.Summary, "…") {
		t.Fatalf("summary not capped at %d runes + ellipsis: len=%d", briefSummaryMax, n)
	}
	if b.Summary != "Short." || b.Parent != nil || b.Aliases != nil {
		t.Fatalf("brief b = %+v", b)
	}
}
