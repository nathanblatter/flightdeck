// Package public is the internet-facing connector surface: what claude.ai and
// the Claude mobile app reach through the tunnel at FLIGHTDECK_PUBLIC_URL.
//
// Flightdeck is a plain RFC 9728 protected resource here. Squawk
// (squawk.nathanblatter.com) is the authorization server: it registers the
// connector, Nathan approves it once in the Squawk console, and it mints the
// ES256 tokens this package verifies against Squawk's JWKS. Nothing about
// login, clients or tokens lives in flightdeck any more.
package public

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/nathanblatter/squawk/client"

	"flightdeck/internal/auth"
)

// ActorName is the identity every Squawk-authenticated call runs as. One
// connector, one actor: activity rows and usage telemetry attribute to it
// the same way a private caller attributes to its API key name.
const ActorName = "claude-connector"

// resourceName is the human-facing name in the protected-resource document.
const resourceName = "Flightdeck"

// Config is what the public surface needs to know about itself and Squawk.
type Config struct {
	// PublicURL is the https origin the tunnel forwards (no path).
	PublicURL string
	// Issuer is the Squawk origin, e.g. https://squawk.nathanblatter.com.
	Issuer string
	// ServiceID is flightdeck's service id at Squawk; tokens must name it as
	// their audience.
	ServiceID string
}

// Handler builds the mux served under the public hostname and returns it
// with that hostname. The complete surface is the protected-resource
// document and a token-only /mcp; every other path answers 404 whatever the
// tunnel in front chooses to forward.
func Handler(cfg Config, mcpHandler http.Handler, opts ...client.VerifierOption) (http.Handler, string, error) {
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return nil, "", fmt.Errorf("public URL must be an https origin with no path, got %q", cfg.PublicURL)
	}
	if cfg.Issuer == "" {
		return nil, "", fmt.Errorf("auth issuer is required")
	}
	if cfg.ServiceID == "" {
		return nil, "", fmt.Errorf("auth service id is required")
	}
	resource := u.Scheme + "://" + u.Host + "/mcp"
	v := client.NewVerifier(cfg.Issuer, cfg.ServiceID, append([]client.VerifierOption{client.WithResource(resource)}, opts...)...)

	mux := http.NewServeMux()
	mux.Handle("GET /.well-known/oauth-protected-resource/mcp",
		client.ProtectedResourceMetadata(v, resourceName, []string{auth.ScopeRead, auth.ScopeWrite}))
	// MCP tool calls need write (agents get read+write), the same bar the
	// private /mcp sets.
	guard := client.RequireToken(v)(client.RequireScope(auth.ScopeWrite)(bridge(mcpHandler)))
	mux.Handle("/mcp", guard)
	mux.Handle("/mcp/", guard)
	return mux, u.Hostname(), nil
}

// bridge turns the verified Squawk claims into flightdeck's own Identity so
// the MCP server, activity actors and request logging see exactly what they
// would for an API-key caller.
func bridge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := client.FromContext(r.Context())
		if !ok {
			// RequireToken runs first; reaching here without claims is a
			// wiring bug, not a caller error. Fail closed.
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		id := auth.Identity{Name: ActorName, Scopes: claims.Scopes()}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
	})
}

// SplitByHost routes requests for publicHost to pub and everything else to
// private. The tunnel preserves the original Host header, so a request that
// came through it is recognizable by hostname alone; a direct Tailscale
// request carries the IP (or a *.ts.net name) and stays on the private side.
func SplitByHost(publicHost string, pub, private http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if strings.EqualFold(host, publicHost) {
			pub.ServeHTTP(w, r)
			return
		}
		private.ServeHTTP(w, r)
	})
}
