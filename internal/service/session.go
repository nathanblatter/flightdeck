package service

import "context"

type sessionKey struct{}

// WithSessionID tags ctx with the transport-level session identity (the MCP
// Mcp-Session-Id) so telemetry written anywhere below — tool_calls,
// search_log — can be grouped back into the session that produced it.
func WithSessionID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionKey{}, id)
}

// SessionID returns the session identity on ctx, or "" when the caller has
// none (REST requests, or an MCP transport running without session ids).
func SessionID(ctx context.Context) string {
	id, _ := ctx.Value(sessionKey{}).(string)
	return id
}
