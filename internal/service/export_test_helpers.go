package service

import "context"

// VecCacheRoundTrip builds a vec cache from the environment, stores vec under q,
// then reads it back — letting the integration package verify the Redis wiring
// without reaching into unexported fields. Returns (nil,false) when no Redis is
// configured or the round-trip fails.
func VecCacheRoundTrip(ctx context.Context, q string, vec []float32) ([]float32, bool) {
	c := newVecCache("test-model")
	if !c.enabled() {
		return nil, false
	}
	c.set(ctx, q, vec)
	return c.get(ctx, q)
}
