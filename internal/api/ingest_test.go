package api

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSeverityToPriority(t *testing.T) {
	cases := map[string]string{
		"low":      "low",
		"LOW":      "low",
		"  high ":  "high",
		"urgent":   "urgent",
		"critical": "urgent",
		"":         "med",
		"weird":    "med",
	}
	for in, want := range cases {
		if got := severityToPriority(in); got != want {
			t.Errorf("severityToPriority(%q) = %q, want %q", in, got, want)
		}
	}
}

// Titles must be truncated on rune boundaries, never mid-UTF-8-sequence.
func TestBugTitleRuneSafe(t *testing.T) {
	long := strings.Repeat("🐛", 100) // 4 bytes per rune
	got := bugTitle(long)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated title is invalid UTF-8: %q", got)
	}
	if r := []rune(got); len(r) != 80 {
		t.Fatalf("truncated title = %d runes, want 80 (77 + ellipsis)", len(r))
	}
	if short := bugTitle("  short one  "); short != "short one" {
		t.Fatalf("short title = %q, want trimmed passthrough", short)
	}
}
