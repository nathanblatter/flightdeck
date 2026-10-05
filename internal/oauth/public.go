package oauth

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"flightdeck/internal/auth"
	"flightdeck/internal/store"
)

// PublicHandler builds the mux served under the public hostname and returns
// it with that hostname. It is the complete public surface: OAuth, its
// discovery documents, and a bearer-only /mcp. Everything else answers 404
// there, whatever the tunnel in front chooses to forward.
func PublicHandler(st *store.Store, publicURL string, mcpHandler http.Handler) (http.Handler, string, error) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return nil, "", fmt.Errorf("must be an https origin with no path, got %q", publicURL)
	}
	oa := New(st, u.Scheme+"://"+u.Host)
	pm := http.NewServeMux()
	pm.Handle("/.well-known/", oa.Routes())
	pm.Handle("/oauth/", oa.Routes())
	guard := auth.PublicMiddleware(st, auth.ScopeWrite, oa.ResourceMetadataURL())
	pm.Handle("/mcp", guard(mcpHandler))
	pm.Handle("/mcp/", guard(mcpHandler))
	return pm, u.Hostname(), nil
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
