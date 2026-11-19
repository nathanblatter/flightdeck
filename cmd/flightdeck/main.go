package main

import (
	"context"
	// The runtime image is alpine without tzdata; embedding the zone database
	// keeps TZ / FLIGHTDECK_TZ resolvable so date-only due dates land on the
	// owner's calendar day, not UTC.
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
	_ "time/tzdata"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"flightdeck/internal/api"
	"flightdeck/internal/auth"
	"flightdeck/internal/blob"
	mcpserver "flightdeck/internal/mcp"
	"flightdeck/internal/metrics"
	"flightdeck/internal/public"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
	syncengine "flightdeck/internal/sync"
	"flightdeck/internal/update"
	"flightdeck/web"
)

// Version is the build version, overridable via -ldflags "-X main.Version=...".
var Version = "dev"

// defaultAuthIssuer is the Squawk origin used when FLIGHTDECK_AUTH_ISSUER is unset.
const defaultAuthIssuer = "https://squawk.nathanblatter.com"

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
		case "search-eval":
			runSearchEval(os.Args[2:])
			return
		case "version", "--version", "-v":
			fmt.Println(Version)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\nusage: flightdeck [serve|migrate|keygen|keys|up|update|search-eval|version]\n", os.Args[1])
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

	// Public connector surface (claude.ai / Claude mobile). Enabled by
	// FLIGHTDECK_PUBLIC_URL, the https origin a tunnel forwards to this
	// process. Requests arriving under that hostname see a separate, minimal
	// mux: the RFC 9728 protected-resource document and a token-only /mcp.
	// Squawk (FLIGHTDECK_AUTH_ISSUER) is the authorization server; tokens must
	// be addressed to FLIGHTDECK_AUTH_SERVICE_ID. The UI, /api, /metrics and
	// raw API keys never exist on the public side, whatever the tunnel routes.
	var handler http.Handler = mux
	if pub := os.Getenv("FLIGHTDECK_PUBLIC_URL"); pub != "" {
		cfg := public.Config{
			PublicURL: pub,
			Issuer:    os.Getenv("FLIGHTDECK_AUTH_ISSUER"),
			ServiceID: os.Getenv("FLIGHTDECK_AUTH_SERVICE_ID"),
		}
		if cfg.Issuer == "" {
			cfg.Issuer = defaultAuthIssuer
		}
		if cfg.ServiceID == "" {
			log.Fatalf("FLIGHTDECK_PUBLIC_URL is set but FLIGHTDECK_AUTH_SERVICE_ID is empty: set it to flightdeck's service id in the Squawk console (%s)", cfg.Issuer)
		}
		publicMux, host, err := public.Handler(cfg, mcpserver.NewPublicHandler(st, svc, Version, upd))
		if err != nil {
			log.Fatalf("public connector: %v", err)
		}
		log.Printf("public connector enabled for %s (protected resource of %s, service %s; /mcp only)", host, cfg.Issuer, cfg.ServiceID)
		handler = public.SplitByHost(host, publicMux, mux)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           metrics.HTTPMiddleware(withLogging(handler)),
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

	// Cross-instance project sharing. Self-disabling: with no shares
	// configured the loop costs one empty query every interval.
	if instanceID, err := svc.InstanceID(ctx); err != nil {
		log.Printf("sync: no instance identity, sharing disabled: %v", err)
	} else {
		engine := &syncengine.Engine{
			St:         st,
			InstanceID: instanceID,
			// Applied remote changes must invalidate the orient caches and
			// reach open browsers, exactly as a local write would.
			OnChange: func(projectID uuid.UUID) { svc.InvalidateProject(projectID) },
		}
		go engine.Run(ctx)
	}

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
// once. Usage: flightdeck keygen <name> <scope[,scope...]> [expiry-days] [--projects slug[,slug...]]
func runKeygen(args []string) {
	pos, projects, hasProjects, err := splitProjectsFlag(args)
	if err != nil {
		log.Fatal(err)
	}
	if len(pos) < 2 || len(pos) > 3 {
		fmt.Fprintln(os.Stderr, "usage: flightdeck keygen <name> <scope[,scope...]> [expiry-days] [--projects slug[,slug...]]")
		fmt.Fprintln(os.Stderr, "  scopes: read, write, ingest")
		fmt.Fprintln(os.Stderr, "  --projects: restrict the key to these projects (ingest scope only)")
		os.Exit(2)
	}
	name := pos[0]
	scopes := splitScopes(pos[1])
	if len(scopes) == 0 {
		log.Fatal("at least one scope is required")
	}
	if hasProjects {
		if err := auth.CheckRestrictionScopes(scopes); err != nil {
			log.Fatalf("refusing to create key: %v", err)
		}
	}
	var expiresAt *time.Time
	if len(pos) == 3 {
		days, err := strconv.Atoi(pos[2])
		if err != nil || days <= 0 {
			log.Fatalf("invalid expiry-days %q", pos[2])
		}
		t := time.Now().AddDate(0, 0, days)
		expiresAt = &t
	}

	ctx := context.Background()
	st, err := store.NewStore(ctx, mustDatabaseURL())
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	if err := store.Migrate(mustDatabaseURL()); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	var projectIDs []uuid.UUID // nil = unrestricted
	if hasProjects {
		projectIDs, err = service.ResolveKeyProjects(ctx, st, projects)
		if err != nil {
			log.Fatalf("refusing to create key: %v", err)
		}
	}

	raw := newRawKey()
	key, err := st.CreateAPIKey(ctx, store.CreateAPIKeyParams{
		Name:       name,
		KeyHash:    auth.HashKey(raw),
		Scopes:     scopes,
		ExpiresAt:  expiresAt,
		ProjectIds: projectIDs,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			log.Fatal("failed to create key")
		}
		log.Fatalf("create key: %v", err)
	}

	desc, err := service.DescribeKeyProjects(ctx, st, key.ProjectIds)
	if err != nil {
		log.Fatalf("describe projects: %v", err)
	}
	fmt.Printf("created key %q (id=%s) scopes=%v projects=%s\n", key.Name, key.ID, key.Scopes, desc)
	fmt.Println("\n  API KEY (shown once — store it now):")
	fmt.Printf("    %s\n", raw)
}

// splitProjectsFlag pulls `--projects a,b` (or `--projects=a,b`) out of args,
// anywhere in the list, and returns the remaining positional args unchanged so
// the existing `keygen <name> <scopes> [expiry-days]` form keeps working.
func splitProjectsFlag(args []string) (pos, projects []string, found bool, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--projects" || a == "-projects":
			if i+1 >= len(args) {
				return nil, nil, false, errors.New("--projects needs a value: slug[,slug...]")
			}
			if found {
				return nil, nil, false, errors.New("--projects given more than once")
			}
			found = true
			projects = splitScopes(args[i+1])
			i++
		case strings.HasPrefix(a, "--projects=") || strings.HasPrefix(a, "-projects="):
			if found {
				return nil, nil, false, errors.New("--projects given more than once")
			}
			found = true
			projects = splitScopes(a[strings.IndexByte(a, '=')+1:])
		default:
			pos = append(pos, a)
		}
	}
	if found && len(projects) == 0 {
		return nil, nil, false, errors.New("--projects needs at least one project slug")
	}
	return pos, projects, found, nil
}

const keysUsage = "usage: flightdeck keys [list | revoke <id> | rotate <id> | restrict <id> <slug[,slug...]> | unrestrict <id>]"

// runKeys lists, revokes, rotates, or (un)restricts API keys.
func runKeys(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, keysUsage)
		os.Exit(2)
	}
	ctx := context.Background()
	st, err := store.NewStore(ctx, mustDatabaseURL())
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	switch args[0] {
	case "list":
		keys, err := st.ListAPIKeys(ctx)
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
			projects, err := service.DescribeKeyProjects(ctx, st, k.ProjectIds)
			if err != nil {
				log.Fatalf("describe projects: %v", err)
			}
			fmt.Printf("%s  %-20s  %-8s  scopes=%v  projects=%s  last_used=%s\n", k.ID, k.Name, status, k.Scopes, projects, last)
		}
	case "revoke":
		id := keyIDArg(args, "revoke <id>")
		k, err := st.RevokeAPIKey(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				log.Fatalf("no key with id %s", id)
			}
			log.Fatalf("revoke: %v", err)
		}
		fmt.Printf("revoked key %q (id=%s)\n", k.Name, k.ID)
	case "rotate":
		id := keyIDArg(args, "rotate <id>")
		raw := newRawKey()
		k, err := st.RotateAPIKey(ctx, store.RotateAPIKeyParams{ID: id, KeyHash: auth.HashKey(raw)})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				log.Fatalf("no key with id %s", id)
			}
			log.Fatalf("rotate: %v", err)
		}
		fmt.Printf("rotated key %q (id=%s) — old secret is now invalid\n", k.Name, k.ID)
		fmt.Println("\n  NEW API KEY (shown once — store it now):")
		fmt.Printf("    %s\n", raw)
	case "restrict":
		if len(args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: flightdeck keys restrict <id> <slug[,slug...]>")
			os.Exit(2)
		}
		id := keyIDArg(args, "restrict <id> <slug[,slug...]>")
		k, err := service.RestrictAPIKey(ctx, st, id, splitScopes(args[2]))
		if err != nil {
			log.Fatalf("restrict: %v", err)
		}
		desc, err := service.DescribeKeyProjects(ctx, st, k.ProjectIds)
		if err != nil {
			log.Fatalf("describe projects: %v", err)
		}
		fmt.Printf("restricted key %q (id=%s) to projects=%s (secret unchanged)\n", k.Name, k.ID, desc)
	case "unrestrict":
		id := keyIDArg(args, "unrestrict <id>")
		k, err := service.UnrestrictAPIKey(ctx, st, id)
		if err != nil {
			log.Fatalf("unrestrict: %v", err)
		}
		fmt.Printf("unrestricted key %q (id=%s): projects=all\n", k.Name, k.ID)
	default:
		fmt.Fprintf(os.Stderr, "unknown keys subcommand %q\n%s\n", args[0], keysUsage)
		os.Exit(2)
	}
}

// keyIDArg parses args[1] as a key id, exiting with usage on a missing or
// malformed id.
func keyIDArg(args []string, usage string) uuid.UUID {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: flightdeck keys "+usage)
		os.Exit(2)
	}
	id, err := uuid.Parse(args[1])
	if err != nil {
		log.Fatalf("invalid id: %v", err)
	}
	return id
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
