// Package web embeds the built Vite SPA (web/dist) and serves it. The Go binary
// is fully self-contained: the same process serves /api, /mcp, and this UI.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist
var distFS embed.FS

// Handler serves the embedded SPA. Unknown non-asset paths fall back to
// index.html so client-side navigation works without server routes.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			// Not a real file → serve the SPA shell.
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
