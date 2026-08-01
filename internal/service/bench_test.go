package service

import (
	"fmt"
	"testing"

	"flightdeck/internal/dto"
)

// BenchmarkRRFMerge measures the reciprocal-rank fusion that blends the lexical
// and semantic result lists on every search — a pure, allocation-sensitive hot
// path worth guarding against regression.
func BenchmarkRRFMerge(b *testing.B) {
	mk := func(prefix string, n int) []dto.Item {
		out := make([]dto.Item, n)
		for i := range out {
			out[i] = dto.Item{ID: fmt.Sprintf("%s-%d", prefix, i)}
		}
		return out
	}
	// Overlapping id ranges so the merge actually fuses duplicates.
	fts := mk("id", 25)
	sem := mk("id", 25)
	for i := range sem { // shift half so ~half overlap
		sem[i].ID = fmt.Sprintf("id-%d", i+12)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = rrfMerge(func(it dto.Item) string { return it.ID }, fts, sem)
	}
}

// BenchmarkVecEncode measures the query-embedding (de)serialization used by the
// Redis cache on every semantic search that hits or fills the cache.
func BenchmarkVecEncode(b *testing.B) {
	vec := make([]float32, 1536)
	for i := range vec {
		vec[i] = float32(i) * 0.001
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := decodeVec(encodeVec(vec)); !ok {
			b.Fatal("roundtrip failed")
		}
	}
}
