package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"flightdeck/internal/store"

	fsync "flightdeck/internal/sync"
)

// Sharing config, kept in the settings table so it survives a restart and can
// be set from the setup UI rather than an environment variable.
const (
	SettingMailboxURL   = "mailbox_url"
	SettingMailboxAdmin = "mailbox_admin_token"
	SettingMailboxCert  = "mailbox_client_cert"
	SettingMailboxKey   = "mailbox_client_key"
	SettingMailboxCA    = "mailbox_ca_pem"
	SettingInstanceID   = "instance_id"
)

// ErrSharingNotConfigured means this instance has no mailbox host to share
// through yet.
var ErrSharingNotConfigured = errors.New("sharing is not configured: set the mailbox URL and client certificate first")

// MailboxConfig is this instance's credentials for its mailbox host.
//
// The cert fields accept both their own names and the ones a bundle from
// `flightdeck-mailbox issue` uses, so setting up a new instance is a matter of
// posting that file with a url added rather than renaming three fields by hand.
type MailboxConfig struct {
	URL        string `json:"url"`
	AdminToken string `json:"admin_token"`
	ClientCert string `json:"client_cert"`
	ClientKey  string `json:"client_key"`
	CAPEM      string `json:"ca_pem"`

	// Aliases as issued in a bundle file. Normalize() folds them in.
	BundleCert string `json:"cert_pem,omitempty"`
	BundleKey  string `json:"key_pem,omitempty"`
	// Accepted and ignored so a bundle can be posted verbatim: the strict JSON
	// decoder rejects unknown fields, and these two ride along in every bundle.
	BundleName        string `json:"name,omitempty"`
	BundleFingerprint string `json:"ca_fingerprint,omitempty"`
}

// Normalize folds bundle-shaped fields into the canonical ones.
func (c *MailboxConfig) Normalize() {
	if c.ClientCert == "" {
		c.ClientCert = c.BundleCert
	}
	if c.ClientKey == "" {
		c.ClientKey = c.BundleKey
	}
	c.BundleCert, c.BundleKey = "", ""
	c.BundleName, c.BundleFingerprint = "", ""
	c.URL = strings.TrimRight(strings.TrimSpace(c.URL), "/")
}

// Validate reports why a configuration is unusable, so a mistyped setup fails
// with something actionable instead of a TLS error on the next sync tick.
func (c MailboxConfig) Validate() error {
	switch {
	case c.URL == "":
		return errors.New("mailbox url is required (e.g. https://198.51.100.10)")
	case !strings.HasPrefix(c.URL, "https://"):
		return errors.New("mailbox url must be https")
	case c.ClientCert == "":
		return errors.New("client certificate is missing (cert_pem from the bundle)")
	case c.ClientKey == "":
		return errors.New("client key is missing (key_pem from the bundle)")
	case c.CAPEM == "":
		return errors.New("CA certificate is missing (ca_pem from the bundle)")
	}
	// Fail here rather than at the next sync: a malformed pair is a typo now,
	// not a mystery later.
	if _, err := tls.X509KeyPair([]byte(c.ClientCert), []byte(c.ClientKey)); err != nil {
		return fmt.Errorf("certificate and key do not form a valid pair: %w", err)
	}
	if !x509.NewCertPool().AppendCertsFromPEM([]byte(c.CAPEM)) {
		return errors.New("ca_pem is not a valid certificate")
	}
	return nil
}

func (c MailboxConfig) configured() bool {
	return c.URL != "" && c.ClientCert != "" && c.ClientKey != "" && c.CAPEM != ""
}

// MailboxConfig reads the sharing configuration.
func (s *Service) MailboxConfig(ctx context.Context) (MailboxConfig, error) {
	var c MailboxConfig
	get := func(key string) string {
		v, _ := s.settingString(ctx, key)
		return v
	}
	c.URL = strings.TrimRight(get(SettingMailboxURL), "/")
	c.AdminToken = get(SettingMailboxAdmin)
	c.ClientCert = get(SettingMailboxCert)
	c.ClientKey = get(SettingMailboxKey)
	c.CAPEM = get(SettingMailboxCA)
	return c, nil
}

// SetMailboxConfig stores the sharing configuration.
func (s *Service) SetMailboxConfig(ctx context.Context, c MailboxConfig) error {
	for key, val := range map[string]string{
		SettingMailboxURL:   strings.TrimRight(c.URL, "/"),
		SettingMailboxAdmin: c.AdminToken,
		SettingMailboxCert:  c.ClientCert,
		SettingMailboxKey:   c.ClientKey,
		SettingMailboxCA:    c.CAPEM,
	} {
		if err := s.PutSetting(ctx, key, val); err != nil {
			return err
		}
	}
	return nil
}

// InstanceID returns this instance's stable identity, minting one on first use.
//
// Nothing like it existed before sharing: instance_name is a display label and
// is neither unique nor stable. The sync protocol needs a real identity to tell
// its own messages apart from a peer's.
func (s *Service) InstanceID(ctx context.Context) (string, error) {
	if v, ok := s.settingString(ctx, SettingInstanceID); ok && v != "" {
		return v, nil
	}
	id := uuid.NewString()
	if err := s.PutSetting(ctx, SettingInstanceID, id); err != nil {
		return "", err
	}
	return id, nil
}

// ShareProject sets up a bidirectional share for one project and returns the
// invite code for the other instance.
//
// Two mailboxes are created, one per direction, so neither side can read what
// it sent — each drains only its own. The symmetric key is generated here and
// travels only inside the invite; the mailbox host never receives it, which is
// what lets it carry project data it cannot read.
func (s *Service) ShareProject(ctx context.Context, slug, peerName string) (string, error) {
	cfg, err := s.MailboxConfig(ctx)
	if err != nil {
		return "", err
	}
	if !cfg.configured() {
		return "", ErrSharingNotConfigured
	}
	if cfg.AdminToken == "" {
		return "", errors.New("creating a share needs the mailbox admin token")
	}
	project, err := s.St.GetProjectBySlug(ctx, slug)
	if err != nil {
		return "", err
	}

	client, err := fsync.NewClient(cfg.URL, cfg.ClientCert, cfg.ClientKey, cfg.CAPEM)
	if err != nil {
		return "", err
	}

	// One mailbox per direction. "ours" is what we drain; "theirs" is what we
	// write into.
	oursID, oursWrite, oursRead, err := fsync.CreateMailbox(ctx, client, cfg.AdminToken)
	if err != nil {
		return "", fmt.Errorf("create inbound mailbox: %w", err)
	}
	theirsID, theirsWrite, theirsRead, err := fsync.CreateMailbox(ctx, client, cfg.AdminToken)
	if err != nil {
		return "", fmt.Errorf("create outbound mailbox: %w", err)
	}

	secret, err := fsync.NewKey()
	if err != nil {
		return "", err
	}

	// The peer needs its own credentials to reach the host at all.
	peerBundle, err := issuePeerBundle(ctx, cfg, peerName)
	if err != nil {
		return "", fmt.Errorf("issue peer certificate: %w", err)
	}

	if _, err := s.St.CreateProjectShare(ctx, store.CreateProjectShareParams{
		ProjectID:   project.ID,
		PeerName:    peerName,
		MailboxUrl:  cfg.URL,
		SendMailbox: theirsID, SendToken: theirsWrite, // we write into their mailbox
		RecvMailbox: oursID, RecvToken: oursRead, // we drain our own
		Secret:     secret,
		ClientCert: cfg.ClientCert, ClientKey: cfg.ClientKey, CaPem: cfg.CAPEM,
	}); err != nil {
		return "", err
	}
	s.cache.clear()

	// Mirror image for the joiner: they send where we receive, and vice versa.
	invite := fsync.Invite{
		Version:     fsync.ProtocolVersion,
		PeerName:    peerName,
		ProjectSlug: project.Slug,
		ProjectName: project.Name,
		MailboxURL:  cfg.URL,
		SendMailbox: oursID, SendToken: oursWrite,
		RecvMailbox: theirsID, RecvToken: theirsRead,
		Secret:     secret,
		ClientCert: peerBundle.CertPEM,
		ClientKey:  peerBundle.KeyPEM,
		CAPEM:      peerBundle.CAPEM,
	}
	return invite.Encode()
}

// clientBundle mirrors the mailbox host's issue response.
type clientBundle struct {
	Name    string `json:"name"`
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
	CAPEM   string `json:"ca_pem"`
}

// issuePeerBundle asks the mailbox host for credentials for the instance we are
// inviting. This call itself requires our own client certificate, so an invite
// can only be minted by an instance that already has access.
func issuePeerBundle(ctx context.Context, cfg MailboxConfig, name string) (clientBundle, error) {
	var out clientBundle
	client, err := fsync.NewClient(cfg.URL, cfg.ClientCert, cfg.ClientKey, cfg.CAPEM)
	if err != nil {
		return out, err
	}
	if name == "" {
		name = "peer"
	}
	body, _ := json.Marshal(map[string]string{"name": name})
	resp, err := client.PostAdmin(ctx, "/v1/clients", cfg.AdminToken, body)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return out, fmt.Errorf("mailbox host refused to issue a certificate: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return out, json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
}

// AcceptInvite joins a shared project from a pasted invite code.
//
// The local project is created if the slug is free, and reused if a project
// with that slug already exists — that is the normal case when both instances
// already track the same work and are only now linking it.
func (s *Service) AcceptInvite(ctx context.Context, code, localSlug string) (uuid.UUID, error) {
	inv, err := fsync.DecodeInvite(strings.TrimSpace(code))
	if err != nil {
		return uuid.Nil, err
	}
	slug := localSlug
	if slug == "" {
		slug = inv.ProjectSlug
	}

	project, err := s.St.GetProjectBySlug(ctx, slug)
	if err != nil {
		name := inv.ProjectName
		if name == "" {
			name = slug
		}
		project, err = s.St.CreateProject(ctx, store.CreateProjectParams{Slug: slug, Name: name})
		if err != nil {
			return uuid.Nil, fmt.Errorf("create local project %q: %w", slug, err)
		}
	}

	if _, err := s.St.CreateProjectShare(ctx, store.CreateProjectShareParams{
		ProjectID:   project.ID,
		PeerName:    inv.PeerName,
		MailboxUrl:  inv.MailboxURL,
		SendMailbox: inv.SendMailbox, SendToken: inv.SendToken,
		RecvMailbox: inv.RecvMailbox, RecvToken: inv.RecvToken,
		Secret:     inv.Secret,
		ClientCert: inv.ClientCert, ClientKey: inv.ClientKey, CaPem: inv.CAPEM,
	}); err != nil {
		return uuid.Nil, err
	}
	s.cache.clear()
	return project.ID, nil
}

// InvalidateProject drops cached context and notifies live UI subscribers after
// the sync engine applies a peer's changes. Without it an applied change would
// sit invisible behind the orient cache until it expired.
func (s *Service) InvalidateProject(projectID uuid.UUID) {
	s.cache.clear()
	payload, err := json.Marshal(map[string]any{
		"event":      "project.synced",
		"project_id": projectID.String(),
	})
	if err != nil {
		return
	}
	s.hub.Broadcast(payload)
}
