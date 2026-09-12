package sync

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"flightdeck/internal/store"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k, err := NewKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := testKey(t)
	payload, _ := json.Marshal(ItemPayload{ID: "abc", Title: "hello", Body: "world"})
	sealed, err := Seal(key, Envelope{Version: ProtocolVersion, Kind: KindItem, Origin: "inst-1", SentAt: time.Now().UTC(), Payload: payload})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// The mailbox host stores exactly these bytes, so the plaintext must not be
	// recoverable from them.
	if strings.Contains(string(sealed), "hello") {
		t.Fatal("plaintext is visible in the sealed message")
	}
	env, err := Open(key, sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var got ItemPayload
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if got.Title != "hello" || env.Origin != "inst-1" {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	sealed, err := Seal(testKey(t), Envelope{Version: ProtocolVersion, Kind: KindItem, Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := Open(testKey(t), sealed); err == nil {
		t.Fatal("a message decrypted under the wrong key")
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	key := testKey(t)
	sealed, err := Seal(key, Envelope{Version: ProtocolVersion, Kind: KindItem, Payload: json.RawMessage(`{"a":1}`)})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// Flip a bit in the ciphertext. The mailbox host could do exactly this.
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := Open(key, tampered); err == nil {
		t.Fatal("tampered message was accepted")
	}
	if _, err := Open(key, sealed[:5]); err == nil {
		t.Fatal("truncated message was accepted")
	}
}

func TestInviteRoundTrip(t *testing.T) {
	key := testKey(t)
	in := Invite{
		Version: ProtocolVersion, PeerName: "work-laptop", ProjectSlug: "flightdeck",
		ProjectName: "Flightdeck", MailboxURL: "https://10.0.0.1",
		SendMailbox: "aa", SendToken: "bb", RecvMailbox: "cc", RecvToken: "dd",
		Secret: key, ClientCert: "cert", ClientKey: "key", CAPEM: "ca",
	}
	code, err := in.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.HasPrefix(code, "fdshare_") {
		t.Fatalf("invite lacks its prefix: %q", code[:20])
	}
	out, err := DecodeInvite(code)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.SendMailbox != "aa" || out.RecvToken != "dd" || string(out.Secret) != string(key) {
		t.Fatalf("invite lost data: %+v", out)
	}
}

func TestDecodeInviteRejectsGarbage(t *testing.T) {
	for name, code := range map[string]string{
		"empty":       "",
		"no prefix":   "not-an-invite",
		"bad base64":  "fdshare_!!!!",
		"not json":    "fdshare_aGVsbG8",
		"no key":      mustEncode(t, Invite{Version: ProtocolVersion, MailboxURL: "x", SendMailbox: "a", RecvMailbox: "b"}),
		"no mailbox":  mustEncode(t, Invite{Version: ProtocolVersion, Secret: make([]byte, 32)}),
		"old version": mustEncode(t, Invite{Version: 99, Secret: make([]byte, 32), MailboxURL: "x", SendMailbox: "a", RecvMailbox: "b"}),
	} {
		if _, err := DecodeInvite(code); err == nil {
			t.Errorf("%s: accepted an invalid invite", name)
		}
	}
}

func mustEncode(t *testing.T, i Invite) string {
	t.Helper()
	c, err := i.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return c
}

// TestUpdatedAtNotSynced guards the fix for a real echo loop. A BEFORE UPDATE
// trigger sets updated_at = now() on every write, so an applied row can never
// carry the sender's value. If it were part of the payload the two instances
// would hash identical content differently and volley it forever.
func TestUpdatedAtNotSynced(t *testing.T) {
	b, err := json.Marshal(ItemPayload{ID: "x", CreatedAt: time.Now()})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "updated_at") {
		t.Fatal("item payload carries updated_at; this reintroduces the echo loop")
	}
	b, _ = json.Marshal(ProjectPayload{ID: "x"})
	if strings.Contains(string(b), "updated_at") {
		t.Fatal("project payload carries updated_at; this reintroduces the echo loop")
	}
}

// TestContentHashStable: the hash decides whether a row is sent, so equal
// content must always hash equal and any real change must move it.
func TestContentHashStable(t *testing.T) {
	p := ItemPayload{ID: "a", Title: "t", Body: "b", Status: "todo"}
	h1, _ := ContentHash(p)
	h2, _ := ContentHash(p)
	if h1 != h2 {
		t.Fatal("hash is not stable for identical content")
	}
	p.Status = "done"
	h3, _ := ContentHash(p)
	if h1 == h3 {
		t.Fatal("hash did not change when content did")
	}
}

// TestMergeConflictIsDeterministic is the invariant that makes a shared project
// actually shared. Both instances merge independently, so if they disagree by
// even a byte they settle into permanently different states and stop talking —
// each believing it has already sent what it holds.
func TestMergeConflictIsDeterministic(t *testing.T) {
	const idA, idB = "instance-aaa", "instance-bbb"
	local := store.Item{Title: "local title", Body: "local text", Status: "in_progress", Priority: "med", Type: "task"}
	remote := ItemPayload{Title: "remote title", Body: "remote text", Status: "backlog", Priority: "urgent", Type: "task"}

	// A merges B's message; B merges A's. Same inputs, opposite directions.
	fromA := mergeConflict(idA, idB, local, remote)
	mirrorLocal := store.Item{Title: remote.Title, Body: remote.Body, Status: remote.Status, Priority: remote.Priority, Type: remote.Type}
	mirrorRemote := ItemPayload{Title: local.Title, Body: local.Body, Status: local.Status, Priority: local.Priority, Type: local.Type}
	fromB := mergeConflict(idB, idA, mirrorLocal, mirrorRemote)

	ha, _ := ContentHash(fromA)
	hb, _ := ContentHash(fromB)
	if ha != hb {
		t.Fatalf("merge diverged:\n  A: %+v\n  B: %+v", fromA, fromB)
	}
	// Neither version's prose may be discarded.
	if !strings.Contains(fromA.Body, "local text") || !strings.Contains(fromA.Body, "remote text") {
		t.Fatalf("a conflicting edit was lost: %q", fromA.Body)
	}
}

// TestMergeConflictIsIdempotent: the merged text travels back to the peer, which
// is itself mid-conflict. Without containment checks it would append the same
// passage again on every exchange and grow without ever settling.
func TestMergeConflictIsIdempotent(t *testing.T) {
	const idA, idB = "instance-aaa", "instance-bbb"
	local := store.Item{Body: "local text", Status: "todo"}
	remote := ItemPayload{Body: "remote text", Status: "todo"}

	once := mergeConflict(idA, idB, local, remote)
	// Feed the merged result back through as the incoming version.
	twice := mergeConflict(idA, idB, store.Item{Body: local.Body, Status: local.Status}, once)
	if once.Body != twice.Body {
		t.Fatalf("merging twice grew the body:\n  once:  %q\n  twice: %q", once.Body, twice.Body)
	}
	// And from the other side, where the local copy is already the merged text.
	thrice := mergeConflict(idB, idA, store.Item{Body: once.Body, Status: once.Status}, ItemPayload{Body: remote.Body, Status: remote.Status})
	if thrice.Body != once.Body {
		t.Fatalf("peer re-merged the settled body:\n  want: %q\n  got:  %q", once.Body, thrice.Body)
	}
}

// TestMergeKeepsDeletionAndOldestCreation: a deletion on either side is honoured
// rather than resurrected, and an item is as old as its oldest copy.
func TestMergeKeepsDeletionAndOldestCreation(t *testing.T) {
	old := time.Now().Add(-72 * time.Hour).UTC()
	deleted := time.Now().Add(-time.Hour).UTC()
	local := store.Item{CreatedAt: old, DeletedAt: &deleted, Body: "x"}
	remote := ItemPayload{CreatedAt: time.Now().UTC(), Body: "x"}

	got := mergeConflict("a", "b", local, remote)
	if got.DeletedAt == nil {
		t.Error("a deletion was resurrected by the merge")
	}
	if !got.CreatedAt.Equal(old) {
		t.Errorf("created_at moved forward: got %v, want %v", got.CreatedAt, old)
	}
}
