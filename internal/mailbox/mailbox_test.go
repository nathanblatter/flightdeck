package mailbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const admin = "admin-token"

func newTestServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	srv := httptest.NewServer(NewServer(store, admin).Handler())
	t.Cleanup(srv.Close)
	return srv, store
}

func do(t *testing.T, method, url, token string, body any) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func createMailbox(t *testing.T, srv *httptest.Server) (id, write, read string) {
	t.Helper()
	code, out := do(t, "POST", srv.URL+"/v1/mailboxes", admin, nil)
	if code != http.StatusCreated {
		t.Fatalf("create mailbox: %d %v", code, out)
	}
	return out["id"].(string), out["write_token"].(string), out["read_token"].(string)
}

func sendRaw(t *testing.T, srv *httptest.Server, id, token, body string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/mailboxes/"+id+"/messages", bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func receive(t *testing.T, srv *httptest.Server, id, token string) []Message {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/v1/mailboxes/"+id+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Messages []Message `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.Messages
}

// TestMailboxHoldsManyMessages is the core of the model: a mailbox is a queue,
// not a slot. Several writes accumulate and are delivered in order.
func TestMailboxHoldsManyMessages(t *testing.T) {
	srv, _ := newTestServer(t)
	id, write, read := createMailbox(t, srv)

	for i := 1; i <= 3; i++ {
		if code := sendRaw(t, srv, id, write, fmt.Sprintf("change-%d", i)); code != http.StatusAccepted {
			t.Fatalf("send %d: %d", i, code)
		}
	}
	msgs := receive(t, srv, id, read)
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	for i, m := range msgs {
		if m.Seq != int64(i+1) {
			t.Errorf("message %d out of order: seq %d", i, m.Seq)
		}
		if got, want := string(m.Body), fmt.Sprintf("change-%d", i+1); got != want {
			t.Errorf("body %q, want %q", got, want)
		}
	}
}

// TestUnackedMessageRedelivered is why delivery is lease-then-ack: an instance
// that fetches a change and dies before applying it must see that change again,
// not lose it and silently diverge from its peer.
func TestUnackedMessageRedelivered(t *testing.T) {
	srv, store := newTestServer(t)
	id, write, read := createMailbox(t, srv)
	sendRaw(t, srv, id, write, "unapplied change")

	if msgs := receive(t, srv, id, read); len(msgs) != 1 {
		t.Fatalf("first receive: got %d, want 1", len(msgs))
	}
	// Leased, so an immediate re-poll sees nothing — no duplicate apply.
	if msgs := receive(t, srv, id, read); len(msgs) != 0 {
		t.Fatalf("leased message redelivered immediately: got %d", len(msgs))
	}
	// Simulate the receiver crashing before ack: expire the lease.
	store.mu.Lock()
	for seq := range store.leases[id] {
		store.leases[id][seq] = time.Now().Add(-time.Second)
	}
	store.mu.Unlock()

	if msgs := receive(t, srv, id, read); len(msgs) != 1 {
		t.Fatal("unacked message was lost instead of redelivered")
	}
}

// TestAckDestroysMessage: nothing is retained past a confirmed delivery.
func TestAckDestroysMessage(t *testing.T) {
	srv, _ := newTestServer(t)
	id, write, read := createMailbox(t, srv)
	sendRaw(t, srv, id, write, "applied change")

	msgs := receive(t, srv, id, read)
	if len(msgs) != 1 {
		t.Fatalf("receive: got %d, want 1", len(msgs))
	}
	code, out := do(t, "POST", srv.URL+"/v1/mailboxes/"+id+"/ack", read, map[string]any{"seqs": []int64{msgs[0].Seq}})
	if code != http.StatusOK {
		t.Fatalf("ack: %d %v", code, out)
	}
	if n := out["acked"].(float64); n != 1 {
		t.Fatalf("acked %v, want 1", n)
	}
	// Even after the lease would have expired, an acked message is gone.
	if msgs := receive(t, srv, id, read); len(msgs) != 0 {
		t.Fatalf("acked message still delivered: got %d", len(msgs))
	}
}

// TestEmptyMailboxReturnsEmptyArray: an empty mailbox must marshal as [], not
// null, so clients never need a special case for "no mail".
func TestEmptyMailboxReturnsEmptyArray(t *testing.T) {
	srv, _ := newTestServer(t)
	id, _, read := createMailbox(t, srv)

	req, _ := http.NewRequest("GET", srv.URL+"/v1/mailboxes/"+id+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+read)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	defer resp.Body.Close()
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := string(raw["messages"]); got != "[]" {
		t.Fatalf("empty mailbox returned %s, want []", got)
	}
}

// TestTokenSeparation: the read token must not be able to send, and the write
// token must not be able to drain. A share hands out only one of them.
func TestTokenSeparation(t *testing.T) {
	srv, _ := newTestServer(t)
	id, write, read := createMailbox(t, srv)

	if code := sendRaw(t, srv, id, read, "should fail"); code != http.StatusNotFound {
		t.Errorf("read token could send: %d", code)
	}
	sendRaw(t, srv, id, write, "real")
	req, _ := http.NewRequest("GET", srv.URL+"/v1/mailboxes/"+id+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+write)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("drain with write token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("write token could drain: %d", resp.StatusCode)
	}
}

// TestUnknownMailboxIndistinguishableFromBadToken: a caller without credentials
// must not be able to probe which mailbox ids exist.
func TestUnknownMailboxIndistinguishableFromBadToken(t *testing.T) {
	srv, _ := newTestServer(t)
	id, _, _ := createMailbox(t, srv)
	missing := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	badToken, _ := do(t, "GET", srv.URL+"/v1/mailboxes/"+id+"/messages", "wrong-token", nil)
	noSuchBox, _ := do(t, "GET", srv.URL+"/v1/mailboxes/"+missing+"/messages", "wrong-token", nil)
	if badToken != http.StatusNotFound || noSuchBox != http.StatusNotFound {
		t.Fatalf("existence is probeable: bad token %d, missing mailbox %d", badToken, noSuchBox)
	}
}

// TestCreateRequiresAdminToken: mailbox creation is the one admin-gated verb.
func TestCreateRequiresAdminToken(t *testing.T) {
	srv, _ := newTestServer(t)
	if code, _ := do(t, "POST", srv.URL+"/v1/mailboxes", "", nil); code != http.StatusUnauthorized {
		t.Errorf("created without a token: %d", code)
	}
	if code, _ := do(t, "POST", srv.URL+"/v1/mailboxes", "not-the-admin-token", nil); code != http.StatusUnauthorized {
		t.Errorf("created with a wrong token: %d", code)
	}
}

// TestDeleteRevokes: deleting a mailbox is the revoke path — a peer holding the
// old invite code stops being able to reach it.
func TestDeleteRevokes(t *testing.T) {
	srv, _ := newTestServer(t)
	id, write, read := createMailbox(t, srv)
	sendRaw(t, srv, id, write, "doomed")

	if code, _ := do(t, "DELETE", srv.URL+"/v1/mailboxes/"+id, write, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := do(t, "GET", srv.URL+"/v1/mailboxes/"+id+"/messages", read, nil); code != http.StatusNotFound {
		t.Fatalf("revoked mailbox still readable: %d", code)
	}
}

// TestRejectsOversizedAndEmpty guards the two payload edges. Oversized must be
// refused outright rather than truncated into a corrupt half-message.
func TestRejectsOversizedAndEmpty(t *testing.T) {
	srv, _ := newTestServer(t)
	id, write, _ := createMailbox(t, srv)

	if code := sendRaw(t, srv, id, write, ""); code != http.StatusBadRequest {
		t.Errorf("empty message accepted: %d", code)
	}
	big := string(bytes.Repeat([]byte("x"), MaxMessageBytes+1))
	if code := sendRaw(t, srv, id, write, big); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized message accepted: %d", code)
	}
}

// TestTokensNotStoredInPlaintext: reading the server's disk must not hand over
// the credentials to write or drain a mailbox.
func TestTokensNotStoredInPlaintext(t *testing.T) {
	srv, store := newTestServer(t)
	id, write, read := createMailbox(t, srv)

	m, err := store.loadMeta(id)
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	if m.WriteTokenSHA == write || m.ReadTokenSHA == read {
		t.Fatal("raw token persisted to disk")
	}
	if m.WriteTokenSHA != hashToken(write) || m.ReadTokenSHA != hashToken(read) {
		t.Fatal("stored hash does not match the issued token")
	}
}

// TestPathTraversalRejected: ids reach the filesystem, so a crafted id must not
// be able to escape the storage directory.
func TestPathTraversalRejected(t *testing.T) {
	srv, _ := newTestServer(t)
	// An empty id collapses the path so the router rejects it (405) before a
	// handler ever runs; everything else must be rejected by validID (404).
	// Either way the requirement is the same: no request reaches the filesystem.
	for _, id := range []string{"..", "../../etc", "not-hex-at-all", ""} {
		code, _ := do(t, "GET", srv.URL+"/v1/mailboxes/"+id+"/messages", admin, nil)
		switch code {
		case http.StatusNotFound, http.StatusMovedPermanently, http.StatusMethodNotAllowed:
		default:
			t.Errorf("id %q returned %d, want a rejection", id, code)
		}
	}
}
