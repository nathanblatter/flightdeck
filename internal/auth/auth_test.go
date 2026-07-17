package auth

import (
	"context"
	"testing"
)

func TestHasScope(t *testing.T) {
	id := Identity{Name: "agent", Scopes: []string{ScopeRead, ScopeWrite}}
	if !id.HasScope(ScopeRead) || !id.HasScope(ScopeWrite) {
		t.Error("expected read+write scopes")
	}
	if id.HasScope(ScopeIngest) {
		t.Error("did not expect ingest scope")
	}
}

func TestActorRef(t *testing.T) {
	// Without a ref cell, ActorRef is empty.
	if got := ActorRef(context.Background()); got != "" {
		t.Errorf("expected empty actor, got %q", got)
	}
	// Outer seeds the cell; inner (auth) fills it; outer reads it back.
	ctx := WithActorRef(context.Background())
	setActorRef(ctx, "site widget")
	if got := ActorRef(ctx); got != "site widget" {
		t.Errorf("got %q, want 'site widget'", got)
	}
}

func TestHashKeyStable(t *testing.T) {
	if HashKey("fd_abc") != HashKey("fd_abc") {
		t.Error("HashKey should be deterministic")
	}
	if HashKey("fd_abc") == HashKey("fd_xyz") {
		t.Error("different keys should hash differently")
	}
}
