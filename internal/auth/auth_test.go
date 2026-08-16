package auth

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestHasScope(t *testing.T) {
	id := Identity{Name: "agent", Scopes: []string{ScopeRead, ScopeWrite}}
	cases := []struct {
		name  string
		scope string
		want  bool
	}{
		{"has read", ScopeRead, true},
		{"has write", ScopeWrite, true},
		{"lacks ingest", ScopeIngest, false},
		{"no substring match", "rea", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := id.HasScope(tc.scope); got != tc.want {
				t.Errorf("HasScope(%q) = %v, want %v", tc.scope, got, tc.want)
			}
		})
	}
}

func TestActorRef(t *testing.T) {
	t.Run("empty without cell", func(t *testing.T) {
		if got := ActorRef(context.Background()); got != "" {
			t.Errorf("expected empty actor, got %q", got)
		}
	})
	t.Run("outer reads what auth fills in", func(t *testing.T) {
		// Outer seeds the cell; inner (auth) fills it; outer reads it back.
		ctx := WithActorRef(context.Background())
		setActorRef(ctx, "site widget")
		if got := ActorRef(ctx); got != "site widget" {
			t.Errorf("got %q, want 'site widget'", got)
		}
	})
}

func TestHashKeyStable(t *testing.T) {
	a, b := HashKey("fd_abc"), HashKey("fd_abc")
	if a != b {
		t.Error("HashKey should be deterministic")
	}
	if HashKey("fd_xyz") == a {
		t.Error("different keys should hash differently")
	}
}

// Error bodies must stay valid JSON even when the message contains quotes or
// other JSON metacharacters.
func TestWriteAuthErrorEscapesJSON(t *testing.T) {
	cases := []struct {
		name, msg string
	}{
		{"plain", "missing X-API-Key"},
		{"embedded quote", `key lacks scope: "write"`},
		{"backslash and newline", "bad\\input\nline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeAuthError(rec, 401, tc.msg)
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
			}
			if body.Error != tc.msg {
				t.Errorf("round-tripped %q, want %q", body.Error, tc.msg)
			}
		})
	}
}
