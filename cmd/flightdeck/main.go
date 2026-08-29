package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"flightdeck/internal/api"
	"flightdeck/internal/auth"
	"flightdeck/internal/blob"
	mcpserver "flightdeck/internal/mcp"
	"flightdeck/internal/metrics"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
	"flightdeck/internal/update"
	"flightdeck/web"
)

// Version is the build version, overridable via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve":
			runServe()
			return
		case "migrate":
			runMigrate()
			return
		case "keygen":
			runKeygen(os.Args[2:])
			return
		case "keys":
			runKeys(os.Args[2:])
			return
		case "up":
			runUp(os.Args[2:])
			return
		case "update":
			runUpdate(os.Args[2:])
			return
		case "version", "--version", "-v":
			fmt.Println(Version)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\nusage: flightdeck [serve|migrate|keygen|keys|up|update|version]\n", os.Args[1])
			os.Exit(2)
		}
	}
	runServe()
}

func mustDatabaseURL() string {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL is required")
	}
	return url
}

func runMigrate() {
	if err := store.Migrate(mustDatabaseURL()); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("migrations applied")
}

func runServe() {
	databaseURL := mustDatabaseURL()
	addr := os.Getenv("FLIGHTDECK_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	if err := store.Migrate(databaseURL); err != nil {
		log.Fatalf("startup migrate: %v", err)
	}

	ctx := context.Background()
	st, err := store.NewStore(ctx, databaseURL)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	svc := service.New(st)

	// Object storage for screenshot attachments (MinIO / any S3 endpoint).
	// Optional: without it the instance runs fine, uploads answer 503.
	if bs, err := blob.NewFromEnv(ctx); err != nil {
		log.Fatalf("blob store: %v", err)
	} else if bs != nil {
		svc.SetBlob(bs)
		log.Printf("attachments enabled (bucket %s)", bs.Bucket())
	} else {
		log.Printf("attachments disabled (FLIGHTDECK_S3_ENDPOINT not set)")
	}

	apiSrv := api.New(st, svc)
	apiSrv.Version = Version

	// Release watcher: polls GitHub for a newer tagged release and feeds the
	// upgrade notice into MCP orient responses and /api/setup/status. Nil when
	// disabled (FLIGHTDECK_UPDATE_REPO=off) — every consumer is nil-safe.
	upd := update.New(Version)
	apiSrv.Upd = upd

	// First-run setup: a fresh instance (no keys yet) needs a one-time token so
	// the SPA wizard can authenticate. `flightdeck up` provides it via env; a
	// bare `serve` generates one and logs it. Instances that are already set up
	// (any active key, e.g. every pre-wizard deployment) skip this entirely.
	if done, err := svc.SetupComplete(ctx); err != nil {
		log.Fatalf("setup check: %v", err)
	} else if !done {
		token := os.Getenv("FLIGHTDECK_SETUP_TOKEN")
		if token == "" {
			token, err = auth.NewRawKey("fdsetup_")
			if err != nil {
				log.Fatalf("setup token: %v", err)
			}
			log.Printf("first-run setup pending — open the UI and paste this setup token: %s", token)
		} else {
			log.Printf("first-run setup pending — open the UI and paste the setup token from FLIGHTDECK_SETUP_TOKEN (in the instance .env)")
		}
		apiSrv.SetupToken = token
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := st.Pool.Ping(r.Context()); err != nil {
			http.Error(w, "db unreachable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/api/", apiSrv.Routes())

	// Prometheus metrics — Tailscale-only bind, so left unauthenticated like /healthz.
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		metrics.WritePrometheus(w)
	})

	// MCP streamable-HTTP, gated behind a write-capable key (agents get read+write).
	mcpHandler := mcpserver.NewHandler(st, svc, Version, upd)
	mux.Handle("/mcp", auth.Middleware(st, auth.ScopeWrite)(mcpHandler))
	mux.Handle("/mcp/", auth.Middleware(st, auth.ScopeWrite)(mcpHandler))

	// Embedded SPA (and /bug-widget.js) at the root. Static assets are public;
	// the app authenticates its own /api calls with the user's key. The widget
	// script honors the bug_widget feature flag so a work instance can turn the
	// public embed surface off.
	spa := web.Handler()
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bug-widget.js" && !svc.Flag(service.FlagBugWidget) {
			http.NotFound(w, r)
			return
		}
		spa.ServeHTTP(w, r)
	}))

	srv := &http.Server{
		Addr:              addr,
		Handler:           metrics.HTTPMiddleware(withLogging(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Background workers: durable webhook delivery, daily retention sweeps, and
	// the embedding backfill for semantic search. All stop when the shutdown
	// signal cancels ctx.
	go svc.RunWebhookWorker(ctx)
	go svc.RunMaintenance(ctx)
	go svc.RunEmbedder(ctx)
	go upd.Run(ctx)

	go func() {
		log.Printf("flightdeck %s listening on %s", Version, addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// statusRecorder captures the response status for request logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush propagates through to the underlying writer so SSE (GET /api/stream)
// can push events past this logging wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withLogging logs method, path, status, latency, and the authenticated actor
// (resolved by inner auth middleware) for each request. Health checks and
// Prometheus scrapes are skipped — at one hit each per few seconds they'd
// drown out the requests worth reading.
func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		ctx := auth.WithActorRef(r.Context())
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))
		actor := auth.ActorRef(ctx)
		if actor == "" {
			actor = "-"
		}
		log.Printf("%s %s %d %s actor=%s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond), actor)
	})
}

// runKeygen generates a random API key, stores its hash, and prints the raw key
// once. Usage: flightdeck keygen <name> <scope[,scope...]>
func runKeygen(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: flightdeck keygen <name> <scope[,scope...]> [expiry-days]")
		fmt.Fprintln(os.Stderr, "  scopes: read, write, ingest")
		os.Exit(2)
	}
	name := args[0]
	scopes := splitScopes(args[1])
	if len(scopes) == 0 {
		log.Fatal("at least one scope is required")
	}
	var expiresAt *time.Time
	if len(args) >= 3 {
		days, err := strconv.Atoi(args[2])
		if err != nil || days <= 0 {
			log.Fatalf("invalid expiry-days %q", args[2])
		}
		t := time.Now().AddDate(0, 0, days)
		expiresAt = &t
	}

	raw := newRawKey()
	hash := auth.HashKey(raw)

	st, err := store.NewStore(context.Background(), mustDatabaseURL())
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	if err := store.Migrate(mustDatabaseURL()); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	key, err := st.CreateAPIKey(context.Background(), store.CreateAPIKeyParams{
		Name:      name,
		KeyHash:   hash,
		Scopes:    scopes,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			log.Fatal("failed to create key")
		}
		log.Fatalf("create key: %v", err)
	}

	fmt.Printf("created key %q (id=%s) scopes=%v\n", key.Name, key.ID, key.Scopes)
	fmt.Println("\n  API KEY (shown once — store it now):")
	fmt.Printf("    %s\n", raw)
}

// runKeys lists, revokes, or rotates API keys.
// Usage: flightdeck keys [list|revoke <id>|rotate <id>]
func runKeys(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: flightdeck keys [list|revoke <id>|rotate <id>]")
		os.Exit(2)
	}
	st, err := store.NewStore(context.Background(), mustDatabaseURL())
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	switch args[0] {
	case "list":
		keys, err := st.ListAPIKeys(context.Background())
		if err != nil {
			log.Fatalf("list keys: %v", err)
		}
		for _, k := range keys {
			last := "never"
			if k.LastUsedAt != nil {
				last = k.LastUsedAt.Format(time.RFC3339)
			}
			status := "active"
			if k.Revoked {
				status = "revoked"
			}
			fmt.Printf("%s  %-20s  %-8s  scopes=%v  last_used=%s\n", k.ID, k.Name, status, k.Scopes, last)
		}
	case "revoke":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: flightdeck keys revoke <id>")
			os.Exit(2)
		}
		id, err := uuid.Parse(args[1])
		if err != nil {
			log.Fatalf("invalid id: %v", err)
		}
		k, err := st.RevokeAPIKey(context.Background(), id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				log.Fatalf("no key with id %s", id)
			}
			log.Fatalf("revoke: %v", err)
		}
		fmt.Printf("revoked key %q (id=%s)\n", k.Name, k.ID)
	case "rotate":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: flightdeck keys rotate <id>")
			os.Exit(2)
		}
		id, err := uuid.Parse(args[1])
		if err != nil {
			log.Fatalf("invalid id: %v", err)
		}
		raw := newRawKey()
		k, err := st.RotateAPIKey(context.Background(), store.RotateAPIKeyParams{ID: id, KeyHash: auth.HashKey(raw)})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				log.Fatalf("no key with id %s", id)
			}
			log.Fatalf("rotate: %v", err)
		}
		fmt.Printf("rotated key %q (id=%s) — old secret is now invalid\n", k.Name, k.ID)
		fmt.Println("\n  NEW API KEY (shown once — store it now):")
		fmt.Printf("    %s\n", raw)
	default:
		fmt.Fprintf(os.Stderr, "unknown keys subcommand %q\n", args[0])
		os.Exit(2)
	}
}

func splitScopes(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newRawKey() string {
	raw, err := auth.NewRawKey("fd_")
	if err != nil {
		log.Fatalf("rand: %v", err)
	}
	return raw
}
