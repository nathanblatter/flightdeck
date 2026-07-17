// Package migrations embeds the goose SQL migrations so they ship inside the
// single binary and run on startup (no goose CLI in the container).
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
