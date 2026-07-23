package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"log"
	"math"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

// vecCache caches query embeddings in Redis so a repeated (or refreshed) search
// skips the ~1.4s OpenAI round-trip. Query embeddings are deterministic for a
// given model, so entries are safe to keep for days; keying on the model name
// makes a model change self-invalidating. The whole thing is best-effort: a nil
// client (no REDIS_URL) or any Redis error degrades silently to a live embed,
// exactly like semantic search degrades to lexical.
type vecCache struct {
	rdb   *redis.Client // nil when unconfigured — every method is a no-op
	ttl   time.Duration
	model string
}

// newVecCache builds a cache from FLIGHTDECK_REDIS_URL (falling back to
// REDIS_URL). An empty/invalid URL yields a disabled cache rather than an error.
func newVecCache(model string) *vecCache {
	url := os.Getenv("FLIGHTDECK_REDIS_URL")
	if url == "" {
		url = os.Getenv("REDIS_URL")
	}
	if url == "" {
		return &vecCache{model: model}
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		log.Printf("veccache: invalid REDIS_URL, query-embedding cache disabled: %v", err)
		return &vecCache{model: model}
	}
	return &vecCache{rdb: redis.NewClient(opt), ttl: 7 * 24 * time.Hour, model: model}
}

func (c *vecCache) enabled() bool { return c != nil && c.rdb != nil }

// key namespaces by model so re-embedding under a new model never collides with
// stale vectors, and hashes the query so arbitrary text is a safe Redis key.
func (c *vecCache) key(q string) string {
	sum := sha256.Sum256([]byte(q))
	return "fd:emb:" + c.model + ":" + hex.EncodeToString(sum[:])
}

// get returns a cached vector for q, or (nil,false) on miss/error.
func (c *vecCache) get(ctx context.Context, q string) ([]float32, bool) {
	if !c.enabled() {
		return nil, false
	}
	b, err := c.rdb.Get(ctx, c.key(q)).Bytes()
	if err != nil {
		// redis.Nil (miss) is expected; other errors just mean "embed live".
		return nil, false
	}
	return decodeVec(b)
}

// set stores q's vector for the cache TTL, best-effort.
func (c *vecCache) set(ctx context.Context, q string, vec []float32) {
	if !c.enabled() || len(vec) == 0 {
		return
	}
	if err := c.rdb.Set(ctx, c.key(q), encodeVec(vec), c.ttl).Err(); err != nil {
		log.Printf("veccache: set (continuing): %v", err)
	}
}

// encodeVec packs a float32 slice as little-endian bytes (4 bytes/element).
func encodeVec(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeVec reverses encodeVec, rejecting a payload whose length isn't a whole
// number of float32s (a corrupt/foreign key) rather than returning garbage.
func decodeVec(b []byte) ([]float32, bool) {
	if len(b) == 0 || len(b)%4 != 0 {
		return nil, false
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v, true
}
