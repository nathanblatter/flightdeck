package api

import (
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// corsIngest answers the cross-origin preflight and sets permissive CORS headers
// for the public bug-ingest endpoint only. The embeddable widget POSTs JSON from
// other origins, which triggers an OPTIONS preflight that must NOT require auth.
func corsIngest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ipLimiter is a small in-memory per-IP token bucket (stdlib only) used to throttle
// the public ingest endpoint, whose key is embedded in public HTML and so is spammable.
type ipLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rate     float64 // tokens refilled per second
	burst    float64 // max tokens
}

type visitor struct {
	tokens float64
	last   time.Time
}

func newIPLimiter(ratePerSec float64, burst int) *ipLimiter {
	l := &ipLimiter{visitors: make(map[string]*visitor), rate: ratePerSec, burst: float64(burst)}
	go l.janitor()
	return l
}

// janitor evicts idle visitors so the map doesn't grow unbounded.
func (l *ipLimiter) janitor() {
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

func (l *ipLimiter) allow(ip string) bool {
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

func (l *ipLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the best-effort client address, honoring the first hop of
// X-Forwarded-For when present.
func clientIP(r *http.Request) string {
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
