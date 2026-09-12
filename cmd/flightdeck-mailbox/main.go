// Command flightdeck-mailbox is the rendezvous server for cross-instance
// project sharing. It ships alongside flightdeck but deploys separately: the
// instances themselves are localhost/tailnet-only, so this is the one piece
// that has to be reachable from the open internet.
//
// It never sees project data. Message bodies are encrypted end-to-end by the
// instances, so this process is a courier carrying sealed envelopes.
//
// Access is mutual-TLS: a caller must present a client certificate signed by
// this host's own CA, or the TLS handshake fails before any HTTP is parsed.
// That property holds with this source public, because it rests on possession
// of a private key rather than on anything secret in the code.
//
// Usage:
//
//	flightdeck-mailbox serve          run the server (default)
//	flightdeck-mailbox issue <name>   mint a client bundle for one instance
//	flightdeck-mailbox fingerprint    print the CA fingerprint clients pin
//	flightdeck-mailbox version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"flightdeck/internal/mailbox"
)

// Version is the build version, overridable via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "serve":
		runServe()
	case "issue":
		runIssue()
	case "fingerprint":
		runFingerprint()
	case "version":
		fmt.Println(Version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\nusage: flightdeck-mailbox [serve|issue <name>|fingerprint|version]\n", cmd)
		os.Exit(2)
	}
}

func dataDir() string { return env("MAILBOX_DATA_DIR", "/var/lib/flightdeck-mailbox") }

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func runServe() {
	dir := dataDir()
	addr := env("MAILBOX_ADDR", ":443")
	adminToken := os.Getenv("MAILBOX_ADMIN_TOKEN")
	if adminToken == "" {
		log.Fatal("MAILBOX_ADMIN_TOKEN is required (gates mailbox creation)")
	}
	if len(adminToken) < 32 {
		// The source is public, so this token is the only thing standing between
		// a valid client certificate and unlimited mailbox creation.
		log.Fatal("MAILBOX_ADMIN_TOKEN must be at least 32 characters")
	}

	store, err := mailbox.NewStore(filepath.Join(dir, "mailboxes"))
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	pki := mailbox.NewPKI(filepath.Join(dir, "pki"))
	fingerprint, err := pki.EnsureCA()
	if err != nil {
		log.Fatalf("ca: %v", err)
	}
	sans := strings.Fields(os.Getenv("MAILBOX_SANS"))
	if err := pki.EnsureServerCert(sans); err != nil {
		log.Fatalf("server cert: %v", err)
	}
	tlsCfg, err := pki.ServerTLSConfig()
	if err != nil {
		log.Fatalf("tls: %v", err)
	}

	srv := mailbox.NewServer(store, adminToken).WithPKI(pki)
	httpSrv := &http.Server{
		Addr:      addr,
		Handler:   withClientIdentity(srv.Handler()),
		TLSConfig: tlsCfg,
		// Bounded so a stalled or malicious peer can't pin a connection open
		// indefinitely and exhaust the process.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(handshakeLogFilter{}, "", 0),
	}

	log.Printf("flightdeck-mailbox %s listening on %s (mutual TLS required)", Version, addr)
	log.Printf("data dir: %s", dir)
	log.Printf("CA fingerprint: %s", fingerprint)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	// Certificates come from TLSConfig, so the file arguments are empty.
	if err := httpSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
	log.Print("shut down")
}

// withClientIdentity logs which instance is calling, using the client
// certificate's common name. The server needs no other notion of identity —
// the name is proven by the handshake, not claimed in a header.
func withClientIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := "unknown"
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			name = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		// A panic in a handler must not take down the process and every other
		// instance's sync with it.
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic serving %s %s for %q: %v", r.Method, r.URL.Path, name, rec)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// handshakeLogFilter drops the routine TLS handshake errors that an
// internet-facing port produces constantly — scanners and bots with no client
// certificate. They are the system working as designed; logging each one buries
// real errors and hands an attacker a way to fill the disk.
type handshakeLogFilter struct{}

func (handshakeLogFilter) Write(p []byte) (int, error) {
	msg := string(p)
	switch {
	case strings.Contains(msg, "tls: client didn't provide a certificate"),
		strings.Contains(msg, "tls: bad certificate"),
		strings.Contains(msg, "tls: first record does not look like a TLS handshake"),
		strings.Contains(msg, "tls: unknown certificate authority"),
		strings.Contains(msg, "remote error"),
		strings.Contains(msg, "EOF"):
		return len(p), nil
	}
	log.Print("http: " + strings.TrimSpace(msg))
	return len(p), nil
}

// runIssue mints a client bundle for one flightdeck instance and prints it as
// JSON. This is the only way in: an instance without a bundle cannot complete a
// handshake, no matter what else it knows.
func runIssue() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: flightdeck-mailbox issue <instance-name>")
		os.Exit(2)
	}
	pki := mailbox.NewPKI(filepath.Join(dataDir(), "pki"))
	if _, err := pki.EnsureCA(); err != nil {
		log.Fatalf("ca: %v", err)
	}
	bundle, err := pki.IssueClient(os.Args[2])
	if err != nil {
		log.Fatalf("issue: %v", err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(bundle); err != nil {
		log.Fatalf("encode: %v", err)
	}
	fmt.Fprintf(os.Stderr, "\nissued client certificate for %q — this bundle contains a private key, treat it like one\n", bundle.Name)
}

func runFingerprint() {
	pki := mailbox.NewPKI(filepath.Join(dataDir(), "pki"))
	fp, err := pki.CAFingerprint()
	if err != nil {
		log.Fatalf("fingerprint: %v", err)
	}
	fmt.Println(fp)
}
