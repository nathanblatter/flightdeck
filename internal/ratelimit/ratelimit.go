// Package ratelimit is a small in-memory per-IP token bucket (stdlib only)
// for throttling the public, unauthenticated surfaces: the bug-ingest endpoint
// (its key is embedded in public HTML) and the OAuth login/token endpoints
// (reachable from the internet by design).
package ratelimit

import (
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type visitor struct {
	tokens float64
	last   time.Time
}

// IPLimiter allows `burst` requests at once and refills `rate` per second,
// tracked per client IP.
type IPLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rate     float64
	burst    float64
}

// New returns a limiter whose idle visitors are evicted every 10 minutes so
// the map does not grow unbounded.
func New(ratePerSec float64, burst int) *IPLimiter {
	l := &IPLimiter{visitors: make(map[string]*visitor), rate: ratePerSec, burst: float64(burst)}
	go l.janitor()
	return l
}

func (l *IPLimiter) janitor() {
	for range time.Tick(10 * time.Minute) {
		l.mu.Lock()
		for ip, v := range l.visitors {
			if time.Since(v.last) > 10*time.Minute {
				delete(l.visitors, ip)
			}
		}
		l.mu.Unlock()
	}
}

// Allow reports whether ip may make a request now and spends a token if so.
func (l *IPLimiter) Allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	v, ok := l.visitors[ip]
	if !ok {
		l.visitors[ip] = &visitor{tokens: l.burst - 1, last: now}
		return true
	}
	v.tokens = math.Min(l.burst, v.tokens+now.Sub(v.last).Seconds()*l.rate)
	v.last = now
	if v.tokens >= 1 {
		v.tokens--
		return true
	}
	return false
}

// Middleware answers 429 (via reject) when the client is over its budget.
func (l *IPLimiter) Middleware(reject func(http.ResponseWriter), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(ClientIP(r)) {
			reject(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClientIP returns the best-effort client address, honoring the first hop of
// X-Forwarded-For when present (cloudflared and other proxies set it).
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
