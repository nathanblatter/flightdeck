package service

import (
	"strings"
	"testing"
)

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"Screen Shot 2026-08-03.png": "Screen-Shot-2026-08-03.png",
		"../../etc/passwd":           "passwd",
		"..\\..\\evil.png":           "evil.png",
		"héllo wörld.png":            "hllo-wrld.png",
		"":                           "file",
		"...":                        "file",
		"🐛🐛🐛":                        "file",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	long := sanitizeFilename(strings.Repeat("b", 200) + ".png")
	if len(long) > 100 || !strings.HasSuffix(long, ".png") {
		t.Errorf("long name not bounded with extension kept: %q", long)
	}
}
