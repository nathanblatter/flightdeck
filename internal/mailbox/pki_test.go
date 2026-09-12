package mailbox

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// newMTLSServer starts a mailbox behind mutual TLS, exactly as it runs in
// production, and returns the server plus a bundle for one legitimate instance.
func newMTLSServer(t *testing.T) (*httptest.Server, *PKI, ClientBundle) {
	t.Helper()
	dir := t.TempDir()
	pki := NewPKI(dir)
	if _, err := pki.EnsureCA(); err != nil {
		t.Fatalf("ca: %v", err)
	}
	if err := pki.EnsureServerCert([]string{"127.0.0.1"}); err != nil {
		t.Fatalf("server cert: %v", err)
	}
	tlsCfg, err := pki.ServerTLSConfig()
	if err != nil {
		t.Fatalf("tls config: %v", err)
	}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	srv := httptest.NewUnstartedServer(NewServer(store, admin).Handler())
	srv.TLS = tlsCfg
	srv.StartTLS()
	t.Cleanup(srv.Close)

	bundle, err := pki.IssueClient("test-instance")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return srv, pki, bundle
}

func clientFor(t *testing.T, bundle ClientBundle) *http.Client {
	t.Helper()
	cfg, err := ClientTLSConfig(bundle)
	if err != nil {
		t.Fatalf("client tls: %v", err)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: 10 * time.Second}
}

// TestIssuedInstanceCanConnect is the positive control: a flightdeck instance
// holding a bundle from this host's CA gets through.
func TestIssuedInstanceCanConnect(t *testing.T) {
	srv, _, bundle := newMTLSServer(t)
	resp, err := clientFor(t, bundle).Get(srv.URL + "/v1/healthz")
	if err != nil {
		t.Fatalf("legitimate instance rejected: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("healthz: %d %q", resp.StatusCode, body)
	}
}

// TestNoClientCertRejected is the headline requirement: only valid flightdeck
// instances can talk to this. A caller with no client certificate — every
// scanner, bot, and curl on the internet — fails the handshake and never
// reaches a route, even knowing the exact URL from the public source.
func TestNoClientCertRejected(t *testing.T) {
	srv, _, bundle := newMTLSServer(t)

	// Trusts the right CA, so the server is authenticated — but presents no
	// certificate of its own. This is the plain-curl case.
	cfg, err := ClientTLSConfig(bundle)
	if err != nil {
		t.Fatalf("client tls: %v", err)
	}
	cfg.Certificates = nil
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: 10 * time.Second}

	if _, err := c.Get(srv.URL + "/v1/healthz"); err == nil {
		t.Fatal("a client with no certificate reached the server")
	}
}

// TestSelfSignedClientCertRejected: an attacker who reads this source can mint
// themselves a perfectly well-formed client certificate. It must not work —
// the trust is in the CA signature, not the shape of the certificate.
func TestSelfSignedClientCertRejected(t *testing.T) {
	srv, _, bundle := newMTLSServer(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		// Same subject a real bundle would carry — impersonation attempt.
		Subject:     pkix.Name{CommonName: "test-instance", Organization: []string{"flightdeck"}},
		NotBefore:   time.Now().Add(-time.Hour),
		NotAfter:    time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	forged := ClientBundle{
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})),
		CAPEM:   bundle.CAPEM,
	}
	if _, err := clientFor(t, forged).Get(srv.URL + "/v1/healthz"); err == nil {
		t.Fatal("a self-signed client certificate was accepted")
	}
}

// TestCertFromAnotherCARejected: a bundle issued by a different mailbox host is
// not valid here. Each host trusts only its own CA.
func TestCertFromAnotherCARejected(t *testing.T) {
	srv, _, bundle := newMTLSServer(t)

	other := NewPKI(t.TempDir())
	if _, err := other.EnsureCA(); err != nil {
		t.Fatalf("other ca: %v", err)
	}
	foreign, err := other.IssueClient("test-instance")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Keep our CA for server verification; swap only the client identity.
	foreign.CAPEM = bundle.CAPEM
	if _, err := clientFor(t, foreign).Get(srv.URL + "/v1/healthz"); err == nil {
		t.Fatal("a certificate from a foreign CA was accepted")
	}
}

// TestClientRejectsWrongServer: pinning cuts both ways. An instance must refuse
// to hand its encrypted mail to a server that isn't the host it was issued for,
// so a hijacked IP can't collect messages.
func TestClientRejectsWrongServer(t *testing.T) {
	_, _, bundle := newMTLSServer(t)

	// A second host with its own CA, impersonating the address.
	imposterPKI := NewPKI(t.TempDir())
	if _, err := imposterPKI.EnsureCA(); err != nil {
		t.Fatalf("imposter ca: %v", err)
	}
	if err := imposterPKI.EnsureServerCert([]string{"127.0.0.1"}); err != nil {
		t.Fatalf("imposter server cert: %v", err)
	}
	// Accept any client so the only thing under test is server verification.
	cfg, err := imposterPKI.ServerTLSConfig()
	if err != nil {
		t.Fatalf("imposter tls: %v", err)
	}
	cfg.ClientAuth = tls.NoClientCert
	store, _ := NewStore(t.TempDir())
	imposter := httptest.NewUnstartedServer(NewServer(store, admin).Handler())
	imposter.TLS = cfg
	imposter.StartTLS()
	defer imposter.Close()

	if _, err := clientFor(t, bundle).Get(imposter.URL + "/v1/healthz"); err == nil {
		t.Fatal("instance trusted an imposter server")
	}
}

// TestTLS13Only: no legacy peer exists — every client is built from this repo —
// so older protocol versions are simply not offered.
func TestTLS13Only(t *testing.T) {
	srv, _, bundle := newMTLSServer(t)

	cfg, err := ClientTLSConfig(bundle)
	if err != nil {
		t.Fatalf("client tls: %v", err)
	}
	cfg.MinVersion, cfg.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}, Timeout: 10 * time.Second}
	if _, err := c.Get(srv.URL + "/v1/healthz"); err == nil {
		t.Fatal("server negotiated TLS 1.2")
	}
}

// TestServerIdentifiesClientByCertificate: the calling instance's name is
// proven by the handshake rather than claimed in a header, so the server needs
// no separate notion of identity and a client cannot lie about who it is.
func TestServerIdentifiesClientByCertificate(t *testing.T) {
	dir := t.TempDir()
	pki := NewPKI(dir)
	if _, err := pki.EnsureCA(); err != nil {
		t.Fatalf("ca: %v", err)
	}
	if err := pki.EnsureServerCert([]string{"127.0.0.1"}); err != nil {
		t.Fatalf("server cert: %v", err)
	}
	tlsCfg, _ := pki.ServerTLSConfig()

	var seen string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			seen = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = tlsCfg
	srv.StartTLS()
	defer srv.Close()

	bundle, err := pki.IssueClient("work-laptop")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	resp, err := clientFor(t, bundle).Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if seen != "work-laptop" {
		t.Fatalf("server saw client %q, want work-laptop", seen)
	}
}

// TestCAKeyNotWorldReadable: the CA key is the root of all trust — anything
// that can read it can mint itself an instance identity.
func TestCAKeyNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	pki := NewPKI(dir)
	if _, err := pki.EnsureCA(); err != nil {
		t.Fatalf("ca: %v", err)
	}
	info, err := os.Stat(pki.path(caKeyFile))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("CA key mode %o, want 600", perm)
	}
}

// TestIssueRejectsEmptyName keeps unnamed certificates out of the log.
func TestIssueRejectsEmptyName(t *testing.T) {
	pki := NewPKI(t.TempDir())
	if _, err := pki.EnsureCA(); err != nil {
		t.Fatalf("ca: %v", err)
	}
	if _, err := pki.IssueClient("   "); err == nil {
		t.Fatal("issued a certificate with a blank name")
	}
}

// TestFingerprintFormat: the invite code carries this string, so its shape is
// part of the protocol.
func TestFingerprintFormat(t *testing.T) {
	pki := NewPKI(t.TempDir())
	fp, err := pki.EnsureCA()
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	parts := strings.Split(fp, ":")
	if len(parts) != 32 {
		t.Fatalf("fingerprint has %d octets, want 32", len(parts))
	}
	if fp != strings.ToUpper(fp) {
		t.Fatal("fingerprint is not uppercase")
	}
	again, err := pki.CAFingerprint()
	if err != nil || again != fp {
		t.Fatalf("fingerprint not stable across reads: %q vs %q (%v)", fp, again, err)
	}
}
