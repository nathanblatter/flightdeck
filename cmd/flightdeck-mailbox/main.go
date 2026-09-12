// Command flightdeck-mailbox is the rendezvous server for cross-instance
// project sharing. It ships alongside flightdeck but deploys separately: the
// instances themselves are localhost/tailnet-only, so this is the one piece
// that has to be reachable from the open internet.
//
// It never sees project data. Messages are encrypted end-to-end by the
// instances, so this process is a courier carrying sealed envelopes.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
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
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(Version)
		return
	}

	dataDir := env("MAILBOX_DATA_DIR", "/var/lib/flightdeck-mailbox")
	addr := env("MAILBOX_ADDR", ":8443")
	adminToken := os.Getenv("MAILBOX_ADMIN_TOKEN")
	if adminToken == "" {
		log.Fatal("MAILBOX_ADMIN_TOKEN is required (gates mailbox creation)")
	}

	store, err := mailbox.NewStore(filepath.Join(dataDir, "mailboxes"))
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	srv := mailbox.NewServer(store, adminToken)

	// There is no domain here — instances reach this by bare IP, and public CAs
	// don't issue for IPs. So the server presents a self-signed cert and the
	// invite code carries its fingerprint for the client to pin. No DNS, no CA,
	// no renewals. TLS is protecting the mailbox tokens and metadata; the
	// message bodies are already encrypted end-to-end underneath it.
	certPath := filepath.Join(dataDir, "cert.pem")
	keyPath := filepath.Join(dataDir, "key.pem")
	fingerprint, err := ensureCert(certPath, keyPath, strings.Fields(os.Getenv("MAILBOX_SANS")))
	if err != nil {
		log.Fatalf("tls: %v", err)
	}

	log.Printf("flightdeck-mailbox %s listening on %s", Version, addr)
	log.Printf("data dir: %s", dataDir)
	log.Printf("CERT FINGERPRINT (goes in every invite code):\n    %s", fingerprint)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	if err := httpSrv.ListenAndServeTLS(certPath, keyPath); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
	log.Print("shut down")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ensureCert returns the SHA-256 fingerprint of the server certificate,
// generating a self-signed one on first run. The cert is persisted so the
// fingerprint stays stable across restarts — it is baked into invite codes that
// clients have already stored, and rotating it invalidates every share.
func ensureCert(certPath, keyPath string, sans []string) (string, error) {
	if der, err := readCertDER(certPath); err == nil {
		return fingerprintOf(der), nil
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return "", err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "flightdeck-mailbox"},
		NotBefore:    time.Now().Add(-time.Hour),
		// Long-lived on purpose: clients pin the fingerprint rather than trust a
		// chain, so expiry buys nothing and a rotation would break every
		// outstanding invite code.
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, s := range sans {
		if ip := net.ParseIP(s); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, s)
		}
	}
	if len(tmpl.IPAddresses) == 0 && len(tmpl.DNSNames) == 0 {
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o600); err != nil {
		return "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return "", err
	}
	log.Printf("generated self-signed certificate at %s", certPath)
	return fingerprintOf(der), nil
}

func readCertDER(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("no PEM block in certificate")
	}
	return blk.Bytes, nil
}

// fingerprintOf formats the cert digest the way the invite code carries it:
// colon-separated uppercase hex, the same shape browsers and openssl print.
func fingerprintOf(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(h)/2)
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

func writePEM(path, blockType string, der []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}
