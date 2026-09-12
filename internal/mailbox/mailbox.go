// Package mailbox implements the rendezvous server for cross-instance project
// sharing. It is deliberately the dumbest component in the system, and it has
// no way to read what it carries.
//
// The model is a mailbox, not a database. Flightdeck instances only accept
// localhost/tailnet traffic, so neither end can dial the other; both instead
// reach outward here. Writing to a shared project drops a message in the peer's
// mailbox, and each instance drains its own mailbox on a short timer. Payloads
// are encrypted end-to-end by the clients with a key this server never sees, so
// a compromised mailbox host leaks metadata — which mailbox, how often, how
// big — and nothing else.
//
// Delivery is lease-then-ack rather than delete-on-read. A message is hidden
// when handed out and only destroyed once the receiver confirms it applied it;
// an instance that crashes mid-apply sees the message again instead of silently
// losing a change and diverging from its peer forever. Nothing is retained past
// a confirmed delivery, which is the property that matters.
package mailbox

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// MaxMessageBytes caps one encrypted message. Individual changes are small;
	// the ceiling exists for the periodic full-state message a fresh subscriber
	// needs to bootstrap from.
	MaxMessageBytes = 8 << 20
	// MaxPending bounds an undrained mailbox. A peer that has been offline for
	// a long time stops accumulating rather than filling the disk; the sender
	// sees the backpressure and falls back to sending full state once the peer
	// returns, which is cheaper than replaying a huge backlog anyway.
	MaxPending = 5000
	// MaxMailboxes is a backstop against a leaked admin token, not an abuse
	// control — creation is admin-gated.
	MaxMailboxes = 256
	// LeaseTTL is how long a delivered-but-unacked message stays hidden before
	// it becomes deliverable again. Longer than any sane apply, short enough
	// that a crashed instance recovers within a couple of poll cycles.
	LeaseTTL = 60 * time.Second

	tokenBytes = 32
)

// Meta is the persisted per-mailbox record. Tokens are stored only as SHA-256
// hashes: reading this server's disk must not hand over the credentials to
// write a mailbox or drain it.
type Meta struct {
	ID            string    `json:"id"`
	WriteTokenSHA string    `json:"write_token_sha"`
	ReadTokenSHA  string    `json:"read_token_sha"`
	CreatedAt     time.Time `json:"created_at"`
	NextSeq       int64     `json:"next_seq"`
}

// Message is one encrypted change, opaque to this server.
type Message struct {
	Seq    int64     `json:"seq"`
	SentAt time.Time `json:"sent_at"`
	Body   []byte    `json:"body"`
}

// Store persists mailboxes on the filesystem — one directory each. Deliberately
// not a database: this must deploy to a bare VPS with a binary and a volume.
type Store struct {
	dir string

	mu sync.Mutex
	// leases tracks delivered-but-unacked messages as mailbox -> seq -> expiry.
	// Held in memory on purpose: a restart makes everything deliverable again,
	// which is the safe direction to fail.
	leases map[string]map[int64]time.Time
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create mailbox dir: %w", err)
	}
	return &Store{dir: dir, leases: map[string]map[int64]time.Time{}}, nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// newToken returns a high-entropy random token. Like the main API's keys these
// are random enough that a fast SHA-256 is the right hash — there is no
// low-entropy secret here for a slow KDF to protect.
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// validID rejects anything that could escape the storage directory. IDs are
// server-generated hex, so this guards a value that reaches the filesystem.
func validID(id string) bool {
	if len(id) != tokenBytes*2 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func (s *Store) box(id string) string      { return filepath.Join(s.dir, id) }
func (s *Store) metaPath(id string) string { return filepath.Join(s.box(id), "meta.json") }
func (s *Store) msgPath(id string, seq int64) string {
	// Zero-padded so a lexical directory listing is also sequence order.
	return filepath.Join(s.box(id), fmt.Sprintf("%020d.msg", seq))
}

var ErrNotFound = errors.New("mailbox not found")

func (s *Store) loadMeta(id string) (Meta, error) {
	var m Meta
	if !validID(id) {
		return m, ErrNotFound
	}
	b, err := os.ReadFile(s.metaPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

func (s *Store) saveMeta(m Meta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.metaPath(m.ID), b)
}

// writeFileAtomic writes via a temp file and rename so a crash mid-write can't
// leave a mailbox with unparseable metadata, which would strand it forever.
func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Create mints a mailbox and returns its freshly generated tokens. This is the
// only moment the raw tokens exist — the server keeps hashes, so a lost invite
// code cannot be recovered from here, only rotated.
func (s *Store) Create() (id, writeToken, readToken string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return "", "", "", err
	}
	if len(entries) >= MaxMailboxes {
		return "", "", "", fmt.Errorf("mailbox limit reached (%d)", MaxMailboxes)
	}
	if id, err = newToken(); err != nil {
		return "", "", "", err
	}
	if writeToken, err = newToken(); err != nil {
		return "", "", "", err
	}
	if readToken, err = newToken(); err != nil {
		return "", "", "", err
	}
	if err = os.MkdirAll(s.box(id), 0o700); err != nil {
		return "", "", "", err
	}
	m := Meta{
		ID:            id,
		WriteTokenSHA: hashToken(writeToken),
		ReadTokenSHA:  hashToken(readToken),
		CreatedAt:     time.Now().UTC(),
		NextSeq:       1,
	}
	if err = s.saveMeta(m); err != nil {
		return "", "", "", err
	}
	return id, writeToken, readToken, nil
}

// pendingSeqs returns the sequence numbers currently stored, in order.
func (s *Store) pendingSeqs(id string) ([]int64, error) {
	entries, err := os.ReadDir(s.box(id))
	if err != nil {
		return nil, err
	}
	var seqs []int64
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".msg") {
			continue
		}
		var seq int64
		if _, err := fmt.Sscanf(strings.TrimSuffix(e.Name(), ".msg"), "%d", &seq); err == nil {
			seqs = append(seqs, seq)
		}
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	return seqs, nil
}

// Send appends a message. Returns the assigned sequence number.
func (s *Store) Send(id string, body []byte) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.loadMeta(id)
	if err != nil {
		return 0, err
	}
	seqs, err := s.pendingSeqs(id)
	if err != nil {
		return 0, err
	}
	if len(seqs) >= MaxPending {
		return 0, fmt.Errorf("mailbox full (%d undelivered messages)", MaxPending)
	}
	seq := m.NextSeq
	env := Message{Seq: seq, SentAt: time.Now().UTC(), Body: body}
	b, err := json.Marshal(env)
	if err != nil {
		return 0, err
	}
	// Message first, then the counter bump: a crash between them re-uses a seq
	// for a message that was never durably announced, which is harmless. The
	// reverse order would skip a seq and look like a lost message.
	if err := writeFileAtomic(s.msgPath(id, seq), b); err != nil {
		return 0, err
	}
	m.NextSeq++
	return seq, s.saveMeta(m)
}

// Receive hands out up to limit undelivered messages and leases them. Leased
// messages stay stored until acked — that is what makes a crash mid-apply
// recoverable instead of a silent, permanent divergence between peers.
func (s *Store) Receive(id string, limit int) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.loadMeta(id); err != nil {
		return nil, err
	}
	seqs, err := s.pendingSeqs(id)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	held := s.leases[id]
	if held == nil {
		held = map[int64]time.Time{}
		s.leases[id] = held
	}
	var out []Message
	for _, seq := range seqs {
		if len(out) >= limit {
			break
		}
		if exp, ok := held[seq]; ok && now.Before(exp) {
			continue // still leased to an in-flight delivery
		}
		b, err := os.ReadFile(s.msgPath(id, seq))
		if errors.Is(err, os.ErrNotExist) {
			continue // acked concurrently
		}
		if err != nil {
			return nil, err
		}
		var msg Message
		if err := json.Unmarshal(b, &msg); err != nil {
			// A corrupt message would otherwise wedge the mailbox forever,
			// since it can never be applied and therefore never acked.
			log.Printf("mailbox %s: dropping unparseable message %d: %v", id, seq, err)
			_ = os.Remove(s.msgPath(id, seq))
			continue
		}
		held[seq] = now.Add(LeaseTTL)
		out = append(out, msg)
	}
	return out, nil
}

// Ack destroys messages the receiver confirms it applied. Nothing is retained
// past this point — the mailbox is a courier, not an archive.
func (s *Store) Ack(id string, seqs []int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.loadMeta(id); err != nil {
		return 0, err
	}
	n := 0
	for _, seq := range seqs {
		if err := os.Remove(s.msgPath(id, seq)); err == nil {
			n++
		} else if !errors.Is(err, os.ErrNotExist) {
			return n, err
		}
		delete(s.leases[id], seq)
	}
	return n, nil
}

// Delete removes a mailbox and everything in it — the revoke path. A peer
// holding the old invite code then gets a 404 and stops syncing.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.loadMeta(id); err != nil {
		return err
	}
	delete(s.leases, id)
	return os.RemoveAll(s.box(id))
}

// Server is the HTTP surface. adminToken gates mailbox creation; everything
// else is authorized by the mailbox's own tokens.
type Server struct {
	store      *Store
	adminToken string
	// pki, when set, lets an admin-authenticated instance mint a client bundle
	// for the peer it is inviting. Without this the peer could never complete a
	// handshake, since bundles are only issuable where the CA key lives.
	pki *PKI
}

func NewServer(store *Store, adminToken string) *Server {
	return &Server{store: store, adminToken: adminToken}
}

// WithPKI enables the client-issuing endpoint.
func (s *Server) WithPKI(p *PKI) *Server { s.pki = p; return s }

// Handler mounts the v1 protocol. The version is in the path because this
// eventually ships to strangers on mismatched versions, and the server must be
// able to serve an old client while a new one rolls out.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("POST /v1/mailboxes", s.create)
	mux.HandleFunc("POST /v1/clients", s.issueClient)
	mux.HandleFunc("DELETE /v1/mailboxes/{id}", s.del)
	mux.HandleFunc("POST /v1/mailboxes/{id}/messages", s.send)
	mux.HandleFunc("GET /v1/mailboxes/{id}/messages", s.receive)
	mux.HandleFunc("POST /v1/mailboxes/{id}/ack", s.ack)
	return mux
}

func bearer(r *http.Request) string {
	if after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return after
	}
	return ""
}

// tokenMatches compares in constant time so a caller can't learn a token by
// timing how long a wrong guess takes to reject.
func tokenMatches(presented, wantSHA string) bool {
	return subtle.ConstantTimeCompare([]byte(hashToken(presented)), []byte(wantSHA)) == 1
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	if s.adminToken == "" || subtle.ConstantTimeCompare([]byte(bearer(r)), []byte(s.adminToken)) != 1 {
		writeErr(w, http.StatusUnauthorized, "admin token required")
		return
	}
	id, writeToken, readToken, err := s.store.Create()
	if err != nil {
		log.Printf("mailbox: create: %v", err)
		writeErr(w, http.StatusInsufficientStorage, err.Error())
		return
	}
	log.Printf("mailbox %s created", id)
	writeJSON(w, http.StatusCreated, map[string]string{
		"id": id, "write_token": writeToken, "read_token": readToken,
	})
}

// issueClient mints a client bundle so an instance creating a share can hand
// its peer the credentials to reach this host at all. Admin-gated and, like
// every other route, only reachable by a caller that already completed a mutual
// TLS handshake — so this cannot be used to bootstrap a first way in.
func (s *Server) issueClient(w http.ResponseWriter, r *http.Request) {
	if s.adminToken == "" || subtle.ConstantTimeCompare([]byte(bearer(r)), []byte(s.adminToken)) != 1 {
		writeErr(w, http.StatusUnauthorized, "admin token required")
		return
	}
	if s.pki == nil {
		writeErr(w, http.StatusNotImplemented, "this host cannot issue client certificates")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "decode request")
		return
	}
	bundle, err := s.pki.IssueClient(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	log.Printf("mailbox: issued client certificate for %q", bundle.Name)
	writeJSON(w, http.StatusCreated, bundle)
}

// auth resolves the mailbox and checks the presented token against the
// requested capability. A missing mailbox and a bad token both report 404: a
// caller without credentials must not be able to probe which ids exist.
func (s *Server) auth(w http.ResponseWriter, r *http.Request, write bool) (Meta, bool) {
	m, err := s.store.loadMeta(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "mailbox not found")
		return m, false
	}
	want := m.ReadTokenSHA
	if write {
		want = m.WriteTokenSHA
	}
	if !tokenMatches(bearer(r), want) {
		writeErr(w, http.StatusNotFound, "mailbox not found")
		return m, false
	}
	return m, true
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	m, ok := s.auth(w, r, true)
	if !ok {
		return
	}
	// Read one byte past the cap so an oversized body is rejected outright
	// rather than silently truncated into a corrupt message.
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxMessageBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	if len(body) > MaxMessageBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("message exceeds %d bytes", MaxMessageBytes))
		return
	}
	if len(body) == 0 {
		writeErr(w, http.StatusBadRequest, "empty message")
		return
	}
	seq, err := s.store.Send(m.ID, body)
	if err != nil {
		writeErr(w, http.StatusInsufficientStorage, err.Error())
		return
	}
	// Size and sequence only — the body is ciphertext and is never logged.
	log.Printf("mailbox %s: message %d queued (%d bytes)", m.ID, seq, len(body))
	writeJSON(w, http.StatusAccepted, map[string]any{"seq": seq})
}

func (s *Server) receive(w http.ResponseWriter, r *http.Request) {
	m, ok := s.auth(w, r, false)
	if !ok {
		return
	}
	msgs, err := s.store.Receive(m.ID, 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read messages")
		return
	}
	if msgs == nil {
		// Marshal an empty mailbox as [] rather than null, so a client can
		// always iterate the result without a special case for "no mail".
		msgs = []Message{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) ack(w http.ResponseWriter, r *http.Request) {
	m, ok := s.auth(w, r, false)
	if !ok {
		return
	}
	var req struct {
		Seqs []int64 `json:"seqs"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "decode ack")
		return
	}
	n, err := s.store.Ack(m.ID, req.Seqs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "ack")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"acked": n})
}

func (s *Server) del(w http.ResponseWriter, r *http.Request) {
	m, ok := s.auth(w, r, true)
	if !ok {
		return
	}
	if err := s.store.Delete(m.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete mailbox")
		return
	}
	log.Printf("mailbox %s deleted", m.ID)
	w.WriteHeader(http.StatusNoContent)
}
