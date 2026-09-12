package sync

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrMailboxGone means the mailbox no longer exists — the peer revoked the
// share. Callers disable the share rather than retrying forever.
var ErrMailboxGone = errors.New("mailbox not found (share may have been revoked)")

// Client talks to a mailbox host over mutual TLS. The certificate is not
// optional: without it the handshake fails and no request is ever made.
type Client struct {
	baseURL string
	hc      *http.Client
}

// NewClient builds a mailbox client pinned to the host's own CA. System roots
// are deliberately not trusted, so a public CA mis-issuing for that address
// still cannot impersonate the host and collect our encrypted mail.
func NewClient(baseURL, clientCertPEM, clientKeyPEM, caPEM string) (*Client, error) {
	cert, err := tls.X509KeyPair([]byte(clientCertPEM), []byte(clientKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("client certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caPEM)) {
		return nil, errors.New("share has no usable CA certificate")
	}
	return &Client{
		baseURL: baseURL,
		hc: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					Certificates: []tls.Certificate{cert},
					RootCAs:      pool,
					MinVersion:   tls.VersionTLS13,
				},
				MaxIdleConnsPerHost: 2,
			},
		},
	}, nil
}

func (c *Client) do(ctx context.Context, method, path, token string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return c.hc.Do(req)
}

// Send drops one sealed message into the peer's mailbox.
func (c *Client) Send(ctx context.Context, mailbox, token string, sealed []byte) error {
	resp, err := c.do(ctx, http.MethodPost, "/v1/mailboxes/"+mailbox+"/messages", token, bytes.NewReader(sealed))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrMailboxGone
	}
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("send: %s", statusDetail(resp))
	}
	return nil
}

// Receive drains our own mailbox. Messages stay on the host, leased, until
// Ack confirms they were applied.
func (c *Client) Receive(ctx context.Context, mailbox, token string) ([]MailMessage, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/mailboxes/"+mailbox+"/messages", token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrMailboxGone
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("receive: %s", statusDetail(resp))
	}
	var out struct {
		Messages []MailMessage `json:"messages"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode messages: %w", err)
	}
	return out.Messages, nil
}

// Ack tells the host the messages were applied, which destroys them. Only
// called after a successful local commit — that ordering is the whole reason
// the protocol acks at all.
func (c *Client) Ack(ctx context.Context, mailbox, token string, seqs []int64) error {
	if len(seqs) == 0 {
		return nil
	}
	body, err := json.Marshal(map[string]any{"seqs": seqs})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/v1/mailboxes/"+mailbox+"/ack", token, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ack: %s", statusDetail(resp))
	}
	return nil
}

// PostAdmin calls an admin-gated endpoint on the mailbox host. The caller owns
// closing the response body.
func (c *Client) PostAdmin(ctx context.Context, path, adminToken string, body []byte) (*http.Response, error) {
	return c.do(ctx, http.MethodPost, path, adminToken, bytes.NewReader(body))
}

// MailMessage is one sealed message as the mailbox host returns it.
type MailMessage struct {
	Seq    int64     `json:"seq"`
	SentAt time.Time `json:"sent_at"`
	Body   []byte    `json:"body"`
}

// statusDetail includes the host's error text when it sent one, so a failing
// share reports why instead of just a status code.
func statusDetail(resp *http.Response) string {
	var payload struct {
		Error string `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if json.Unmarshal(b, &payload) == nil && payload.Error != "" {
		return fmt.Sprintf("%s: %s", resp.Status, payload.Error)
	}
	return resp.Status
}

// CreateMailbox provisions a mailbox on the host. Admin-gated, so this is only
// reachable by the instance that holds the host's admin token — normally the
// one creating a share.
func CreateMailbox(ctx context.Context, c *Client, adminToken string) (id, writeToken, readToken string, err error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/mailboxes", adminToken, nil)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", "", "", fmt.Errorf("create mailbox: %s", statusDetail(resp))
	}
	var out struct {
		ID         string `json:"id"`
		WriteToken string `json:"write_token"`
		ReadToken  string `json:"read_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", "", "", err
	}
	return out.ID, out.WriteToken, out.ReadToken, nil
}
