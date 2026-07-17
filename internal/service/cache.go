package service

import (
	"sync"
	"time"
)

// ttlCache is a tiny in-memory cache for the hot orient reads. The TTL is kept
// short and every write clears it, so an agent's write-then-orient never sees
// stale data — the cache only absorbs bursts of reads between writes.
type ttlCache struct {
	mu  sync.Mutex
	m   map[string]cacheEntry
	ttl time.Duration
}

type cacheEntry struct {
	val any
	exp time.Time
}

func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{m: make(map[string]cacheEntry), ttl: ttl}
}

func (c *ttlCache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Now().After(e.exp) {
		return nil, false
	}
	return e.val, true
}

func (c *ttlCache) set(key string, val any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = cacheEntry{val: val, exp: time.Now().Add(c.ttl)}
}

// clear drops every entry — called on any write so reads can't go stale.
func (c *ttlCache) clear() {
	c.mu.Lock()
	c.m = make(map[string]cacheEntry)
	c.mu.Unlock()
}

// Verbosity controls how much item/activity body text orient and list calls
// return. Compact truncates bodies (token economy for agents); full returns
// everything (the web UI and explicit deep reads).
type Verbosity string

const (
	VerbosityCompact Verbosity = "compact"
	VerbosityFull    Verbosity = "full"
)

// bodyLimits maps a verbosity to (item body, activity body) rune caps; 0 = full.
func bodyLimits(v Verbosity) (itemMax, actMax int) {
	if v == VerbosityFull {
		return 0, 0
	}
	return 280, 600
}

// BodyLimits exposes the verbosity body caps to other packages (the MCP layer
// applies them to list/search results outside the service's context builders).
func BodyLimits(v Verbosity) (itemMax, actMax int) { return bodyLimits(v) }
