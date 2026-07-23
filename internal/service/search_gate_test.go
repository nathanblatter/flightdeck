package service

import (
	"math"
	"strings"
	"testing"
)

// Service.wantSemantic needs an emb client that reports Enabled(). We can't build
// a real one without a key, so exercise the two gates it owns (query length and
// lexical recall) directly against the tunables, mirroring wantSemantic's logic
// for the emb-enabled case.
func TestSemanticGates(t *testing.T) {
	cases := []struct {
		name    string
		q       string
		ftsHits int
		want    bool
	}{
		{"fragment j", "j", 0, false},
		{"fragment jo", "jo", 0, false},
		{"three chars, thin lexical", "jou", 0, true},
		{"real query, thin lexical", "journal worker", 1, true},
		{"real query, rich lexical", "journal worker", 5, false},
		{"padded fragment trimmed out", "  j  ", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			longEnough := len([]rune(strings.TrimSpace(c.q))) >= minSemanticQueryLen
			got := longEnough && c.ftsHits < lazySemanticMinFTS
			if got != c.want {
				t.Fatalf("gate(%q, fts=%d) = %v, want %v", c.q, c.ftsHits, got, c.want)
			}
		})
	}
}

func TestVecEncodeRoundTrip(t *testing.T) {
	in := []float32{0, 1, -1, 3.14159, math.MaxFloat32, math.SmallestNonzeroFloat32}
	out, ok := decodeVec(encodeVec(in))
	if !ok {
		t.Fatal("decodeVec rejected a valid payload")
	}
	if len(out) != len(in) {
		t.Fatalf("len = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("element %d: got %v, want %v", i, out[i], in[i])
		}
	}
}

func TestDecodeVecRejectsCorrupt(t *testing.T) {
	for _, b := range [][]byte{nil, {}, {1}, {1, 2, 3}, {1, 2, 3, 4, 5}} {
		if _, ok := decodeVec(b); ok {
			t.Fatalf("decodeVec accepted a %d-byte payload", len(b))
		}
	}
}
