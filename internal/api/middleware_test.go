package api

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	t.Run("x-forwarded-for first hop", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.1")
		if got := clientIP(r); got != "203.0.113.7" {
			t.Errorf("got %q, want 203.0.113.7", got)
		}
	})
	t.Run("remote addr fallback", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = "198.51.100.4:54321"
		if got := clientIP(r); got != "198.51.100.4" {
			t.Errorf("got %q, want 198.51.100.4", got)
		}
	})
}

func TestIPLimiter(t *testing.T) {
	l := newIPLimiter(1, 3) // burst 3
	for i := 0; i < 3; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("request %d should be allowed within burst", i+1)
		}
	}
	if l.allow("1.2.3.4") {
		t.Error("4th request should be throttled")
	}
	if !l.allow("5.6.7.8") {
		t.Error("a different IP should have its own bucket")
	}
}
