package dto

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzTrunc exercises the rune-truncation helper that every compact API response
// runs bodies through. The invariants: it never produces invalid UTF-8 (never
// splits a multi-byte rune), never exceeds the rune cap by more than the single
// ellipsis it appends, is identity when no cut is needed, and never panics on
// any (string, max) — including negative/huge max. This is the class of bug the
// recent "rune-safe ingest titles" fix addressed; the fuzzer guards it for free.
func FuzzTrunc(f *testing.F) {
	for _, s := range []string{"", "hello", "héllo", "日本語テスト", "😀😀😀", "a\x00b"} {
		for _, m := range []int{-1, 0, 1, 2, 3, 100} {
			f.Add(s, m)
		}
	}
	f.Fuzz(func(t *testing.T, s string, max int) {
		// Must not panic for ANY input, including already-invalid UTF-8.
		out := trunc(s, max)

		// trunc is not a UTF-8 sanitizer: on the identity path it returns invalid
		// input unchanged. Real callers pass JSON-sourced (valid) strings, so the
		// meaningful invariant is valid-in → valid-out.
		if !utf8.ValidString(s) {
			return
		}
		if !utf8.ValidString(out) {
			t.Fatalf("trunc(%q, %d) produced invalid UTF-8: %q", s, max, out)
		}
		if max <= 0 {
			if out != s {
				t.Fatalf("max<=0 must be identity: trunc(%q, %d) = %q", s, max, out)
			}
			return
		}
		if utf8.RuneCountInString(s) <= max {
			if out != s {
				t.Fatalf("no cut needed but output changed: trunc(%q, %d) = %q", s, max, out)
			}
			return
		}
		// A real truncation: capped at max content runes + the ellipsis, and it
		// must actually end with that ellipsis.
		if n := utf8.RuneCountInString(out); n > max+1 {
			t.Fatalf("trunc(%q, %d) len=%d exceeds max+1", s, max, n)
		}
		if !strings.HasSuffix(out, "…") {
			t.Fatalf("truncated output missing ellipsis: trunc(%q, %d) = %q", s, max, out)
		}
	})
}
