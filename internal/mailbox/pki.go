package mailbox

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This file implements the mailbox host's private certificate authority.
//
// The server demands a client certificate signed by this CA, so a caller that
// isn't a flightdeck instance is rejected during the TLS handshake — before it
// can send a byte of HTTP, reach a route, or consume a token comparison. That
// is the difference between "the internet can talk to my server and gets 404s"
// and "the internet cannot talk to my server."
//
// It matters that this holds with the source public: the security comes from
// possession of a private key, not from anything an attacker learns by reading
// the code. Scanners, bots, and anyone who knows the exact URL still get
// nothing, because they cannot complete a handshake.
//
// There is no public CA involved and no domain: instances reach the host by
// bare IP, and public CAs don't issue for IPs. The client pins this CA instead.

const (
	caCertFile     = "ca.pem"
	caKeyFile      = "ca-key.pem"
	serverCertFile = "server.pem"
	serverKeyFile  = "server-key.pem"

	// caValidity is long because rotating the CA invalidates every issued
	// client certificate and every outstanding invite at once.
	caValidity     = 10 * 365 * 24 * time.Hour
	clientValidity = 2 * 365 * 24 * time.Hour
)

// PKI manages the mailbox host's CA and the certificates derived from it.
type PKI struct{ dir string }

func NewPKI(dir string) *PKI { return &PKI{dir: dir} }

func (p *PKI) path(name string) string { return filepath.Join(p.dir, name) }

func writePEMFile(path, blockType string, der []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}

func readPEMFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, fmt.Errorf("no PEM block in %s", path)
	}
	return blk.Bytes, nil
}

// serialMax bounds certificate serial numbers at the conventional 128 bits.
var serialMax = new(big.Int).Lsh(big.NewInt(1), 128)

func newSerial() (*big.Int, error) { return rand.Int(rand.Reader, serialMax) }

// Fingerprint is the SHA-256 of a DER certificate, formatted the way openssl
// and browsers print it — colon-separated uppercase hex. This is what an invite
// code carries so a client can pin the CA it expects.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	h := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(h)/2)
	for i := 0; i < len(h); i += 2 {
		parts = append(parts, h[i:i+2])
	}
	return strings.Join(parts, ":")
}

// EnsureCA loads the certificate authority, creating it on first run. Returns
// the CA's fingerprint, which clients pin.
func (p *PKI) EnsureCA() (string, error) {
	if der, err := readPEMFile(p.path(caCertFile)); err == nil {
		return Fingerprint(der), nil
	}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	serial, err := newSerial()
	if err != nil {
		return "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "flightdeck-mailbox CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	if err := writePEMFile(p.path(caCertFile), "CERTIFICATE", der, 0o644); err != nil {
		return "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	// 0600: the CA key is the root of all trust here. Anything that can read it
	// can mint itself a client certificate and walk in.
	if err := writePEMFile(p.path(caKeyFile), "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return "", err
	}
	return Fingerprint(der), nil
}

func (p *PKI) loadCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certDER, err := readPEMFile(p.path(caCertFile))
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := readPEMFile(p.path(caKeyFile))
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.ParseECPrivateKey(keyDER)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// CAFingerprint returns the fingerprint clients pin, without creating anything.
func (p *PKI) CAFingerprint() (string, error) {
	der, err := readPEMFile(p.path(caCertFile))
	if err != nil {
		return "", err
	}
	return Fingerprint(der), nil
}

// EnsureServerCert issues the server's own certificate from the CA, covering
// the given IPs/hostnames. Regenerated when the SAN set changes so moving the
// host to a new address doesn't silently serve an unusable certificate.
func (p *PKI) EnsureServerCert(sans []string) error {
	if der, err := readPEMFile(p.path(serverCertFile)); err == nil {
		if cert, err := x509.ParseCertificate(der); err == nil && sansMatch(cert, sans) && time.Now().Before(cert.NotAfter) {
			return nil
		}
	}
	caCert, caKey, err := p.loadCA()
	if err != nil {
		return fmt.Errorf("load CA: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := newSerial()
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "flightdeck-mailbox"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(caValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	applySANs(tmpl, sans)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	if err := writePEMFile(p.path(serverCertFile), "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return writePEMFile(p.path(serverKeyFile), "EC PRIVATE KEY", keyDER, 0o600)
}

func applySANs(tmpl *x509.Certificate, sans []string) {
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
}

func sansMatch(cert *x509.Certificate, want []string) bool {
	have := map[string]bool{}
	for _, ip := range cert.IPAddresses {
		have[ip.String()] = true
	}
	for _, d := range cert.DNSNames {
		have[d] = true
	}
	if len(want) == 0 {
		return have["127.0.0.1"]
	}
	for _, s := range want {
		if ip := net.ParseIP(s); ip != nil {
			s = ip.String()
		}
		if !have[s] {
			return false
		}
	}
	return true
}

// ClientBundle is everything a flightdeck instance needs to authenticate to
// this mailbox host: its own certificate and key, plus the CA to pin.
type ClientBundle struct {
	Name          string `json:"name"`
	CertPEM       string `json:"cert_pem"`
	KeyPEM        string `json:"key_pem"`
	CAPEM         string `json:"ca_pem"`
	CAFingerprint string `json:"ca_fingerprint"`
}

// IssueClient mints a client certificate for one flightdeck instance. Name is
// recorded in the subject so the server log can say which instance called
// without the server needing any other notion of identity.
func (p *PKI) IssueClient(name string) (ClientBundle, error) {
	var b ClientBundle
	if strings.TrimSpace(name) == "" {
		return b, errors.New("client name is required")
	}
	caCert, caKey, err := p.loadCA()
	if err != nil {
		return b, fmt.Errorf("load CA: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return b, err
	}
	serial, err := newSerial()
	if err != nil {
		return b, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name, Organization: []string{"flightdeck"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(clientValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return b, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return b, err
	}
	return ClientBundle{
		Name:          name,
		CertPEM:       string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:        string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})),
		CAPEM:         string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})),
		CAFingerprint: Fingerprint(caCert.Raw),
	}, nil
}

// ServerTLSConfig requires a client certificate signed by this CA. Everything
// else — scanners, bots, a curl that knows the exact URL — fails the handshake
// and never reaches a route.
func (p *PKI) ServerTLSConfig() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(p.path(serverCertFile), p.path(serverKeyFile))
	if err != nil {
		return nil, fmt.Errorf("load server keypair: %w", err)
	}
	caDER, err := readPEMFile(p.path(caCertFile))
	if err != nil {
		return nil, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		// TLS 1.3 only. Every client is a flightdeck instance built from this
		// repo, so there is no legacy peer to accommodate and no reason to
		// offer older versions or their cipher suites.
		MinVersion: tls.VersionTLS13,
	}, nil
}

// ClientTLSConfig builds the caller side of the same relationship: present our
// certificate, and trust only this CA (not the system roots, so a public CA
// mis-issuing for this IP still can't impersonate the host).
func ClientTLSConfig(bundle ClientBundle) (*tls.Config, error) {
	cert, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	if err != nil {
		return nil, fmt.Errorf("load client keypair: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(bundle.CAPEM)) {
		return nil, errors.New("invalid CA PEM in bundle")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
