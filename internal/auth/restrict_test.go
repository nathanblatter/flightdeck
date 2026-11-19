package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"flightdeck/internal/store"
)

func TestAllowsProject(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	cases := []struct {
		name string
		id   Identity
		p    uuid.UUID
		want bool
	}{
		{"unrestricted allows any", Identity{}, a, true},
		{"restricted allows listed", Identity{Projects: []uuid.UUID{a}}, a, true},
		{"restricted refuses unlisted", Identity{Projects: []uuid.UUID{a}}, b, false},
		{"empty non-nil list allows nothing", Identity{Projects: []uuid.UUID{}}, a, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.id.AllowsProject(tc.p); got != tc.want {
				t.Errorf("AllowsProject = %v, want %v", got, tc.want)
			}
		})
	}
	if (Identity{}).Restricted() {
		t.Error("zero Identity must be unrestricted")
	}
	if !(Identity{Projects: []uuid.UUID{}}).Restricted() {
		t.Error("an empty non-nil list is still a restriction")
	}
}

func TestCheckRestrictionScopes(t *testing.T) {
	if err := CheckRestrictionScopes([]string{ScopeIngest}); err != nil {
		t.Errorf("ingest-only: %v", err)
	}
	for _, scopes := range [][]string{{ScopeRead}, {ScopeWrite}, {ScopeIngest, ScopeRead}, {ScopeRead, ScopeWrite, ScopeIngest}} {
		if err := CheckRestrictionScopes(scopes); err == nil {
			t.Errorf("scopes %v: want refusal", scopes)
		}
	}
}

// fakeKeys serves one key for every hash; TouchAPIKey is a no-op.
type fakeKeys struct{ key store.ApiKey }

func (f fakeKeys) GetAPIKeyByHash(_ context.Context, hash string) (store.ApiKey, error) {
	if hash != HashKey("fd_fake") {
		return store.ApiKey{}, pgx.ErrNoRows
	}
	return f.key, nil
}
func (fakeKeys) TouchAPIKey(context.Context, uuid.UUID) error { return nil }

func serve(t *testing.T, ks keyStore, scope string) (int, string, *Identity) {
	t.Helper()
	var seen *Identity
	h := middleware(ks, scope)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := FromContext(r.Context())
		seen = &id
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-API-Key", "fd_fake")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.Error, seen
}

// The route-class rule holds regardless of the scopes a restricted key has:
// even a (database-forbidden) restricted key with read+write+ingest is refused
// on read and write routes, and passes only on ingest routes.
func TestMiddlewareRefusesRestrictedKeyOutsideIngest(t *testing.T) {
	p := uuid.New()
	ks := fakeKeys{key: store.ApiKey{
		Name: "over-scoped", Scopes: []string{ScopeRead, ScopeWrite, ScopeIngest}, ProjectIds: []uuid.UUID{p},
	}}
	for _, scope := range []string{ScopeRead, ScopeWrite} {
		code, msg, seen := serve(t, ks, scope)
		if code != http.StatusForbidden || msg != ErrRestrictedRoute || seen != nil {
			t.Errorf("%s route: status %d msg %q reached=%v, want 403 %q", scope, code, msg, seen != nil, ErrRestrictedRoute)
		}
	}
	code, _, seen := serve(t, ks, ScopeIngest)
	if code != http.StatusNoContent || seen == nil {
		t.Fatalf("ingest route: status %d, want pass-through", code)
	}
	if !seen.Restricted() || !seen.AllowsProject(p) || seen.AllowsProject(uuid.New()) {
		t.Errorf("identity restriction not carried: %+v", seen)
	}
}

func TestMiddlewareUnrestrictedKeyUnchanged(t *testing.T) {
	ks := fakeKeys{key: store.ApiKey{Name: "agent", Scopes: []string{ScopeRead, ScopeWrite}}}
	for _, scope := range []string{ScopeRead, ScopeWrite} {
		code, _, seen := serve(t, ks, scope)
		if code != http.StatusNoContent || seen == nil || seen.Restricted() {
			t.Errorf("%s route: status %d, want pass-through unrestricted", scope, code)
		}
	}
	code, msg, _ := serve(t, ks, ScopeIngest)
	if code != http.StatusForbidden || msg != "key lacks scope: ingest" {
		t.Errorf("ingest route without scope: %d %q", code, msg)
	}
}
