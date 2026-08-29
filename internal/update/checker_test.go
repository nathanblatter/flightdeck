package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestChecker points a checker at a stub GitHub API.
func newTestChecker(version, apiBase string) *Checker {
	return &Checker{
		version: version,
		repo:    "nathanblatter/flightdeck",
		apiBase: apiBase,
		client:  http.DefaultClient,
	}
}

func stubAPI(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/nathanblatter/flightdeck/releases/latest" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAvailableNewer(t *testing.T) {
	api := stubAPI(t, 200, `{"tag_name":"v0.2.0","html_url":"https://example.com/rel"}`)
	c := newTestChecker("v0.1.0", api.URL)
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}
	rel, ok := c.Available()
	if !ok || rel.Tag != "v0.2.0" || rel.URL != "https://example.com/rel" {
		t.Fatalf("want v0.2.0 available, got %+v ok=%v", rel, ok)
	}
	if n := c.Notice(); n == "" {
		t.Fatal("want a non-empty notice")
	}
}

func TestUpToDate(t *testing.T) {
	api := stubAPI(t, 200, `{"tag_name":"v0.2.0"}`)
	for _, version := range []string{
		"v0.2.0",
		"v0.2.0-3-gabc1234", // commits past the tag compare equal to it
		"v0.3.0",            // ahead of the latest release
	} {
		c := newTestChecker(version, api.URL)
		if err := c.CheckNow(context.Background()); err != nil {
			t.Fatalf("check: %v", err)
		}
		if _, ok := c.Available(); ok {
			t.Errorf("version %s: unexpected update available", version)
		}
		if n := c.Notice(); n != "" {
			t.Errorf("version %s: unexpected notice %q", version, n)
		}
	}
}

func TestUnparsableVersionNeverNags(t *testing.T) {
	api := stubAPI(t, 200, `{"tag_name":"v9.9.9"}`)
	for _, version := range []string{"dev", "docker", "abc1234"} {
		c := newTestChecker(version, api.URL)
		if err := c.CheckNow(context.Background()); err != nil {
			t.Fatalf("check: %v", err)
		}
		if _, ok := c.Available(); ok {
			t.Errorf("version %s: unexpected update available", version)
		}
	}
}

func TestAPIErrorKeepsSnapshot(t *testing.T) {
	good := stubAPI(t, 200, `{"tag_name":"v0.2.0"}`)
	c := newTestChecker("v0.1.0", good.URL)
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}

	bad := stubAPI(t, 500, `boom`)
	c.apiBase = bad.URL
	if err := c.CheckNow(context.Background()); err == nil {
		t.Fatal("want error from 500")
	}
	if rel, ok := c.Available(); !ok || rel.Tag != "v0.2.0" {
		t.Fatalf("snapshot lost after failed check: %+v ok=%v", rel, ok)
	}
}

func TestNoReleasesYet(t *testing.T) {
	api := stubAPI(t, 404, `{"message":"Not Found"}`)
	c := newTestChecker("v0.1.0", api.URL)
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}
	if _, ok := c.Available(); ok {
		t.Fatal("unexpected update with no releases published")
	}
}

func TestNilChecker(t *testing.T) {
	var c *Checker
	if _, ok := c.Available(); ok {
		t.Fatal("nil checker reported an update")
	}
	if n := c.Notice(); n != "" {
		t.Fatalf("nil checker notice %q", n)
	}
	// Run must return immediately on a nil receiver.
	c.Run(context.Background())
}

func TestLatestTag(t *testing.T) {
	tag, ok := LatestTag([]string{"v0.1.0", "v0.10.2", "v0.2.9", "junk", "v1.0"})
	if !ok || tag != "v0.10.2" {
		t.Fatalf("LatestTag = %q ok=%v, want v0.10.2", tag, ok)
	}
	if _, ok := LatestTag([]string{"junk", "release-1"}); ok {
		t.Fatal("LatestTag matched a non-semver tag")
	}
}

func TestSameVersion(t *testing.T) {
	if !SameVersion("v0.1.0-3-gabc1234", "v0.1.0") {
		t.Fatal("describe suffix should compare equal to its base tag")
	}
	if SameVersion("v0.1.0", "v0.1.1") || SameVersion("dev", "dev") {
		t.Fatal("unexpected SameVersion match")
	}
}

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"v1.2.3", true},
		{"1.2.3", true},
		{"v1.2.3-4-gabc1234", true},
		{"v1.2.3-dirty", true},
		{"v1.2", false},
		{"dev", false},
		{"docker", false},
		{"gabc1234", false},
	}
	for _, tc := range cases {
		if _, ok := parseSemver(tc.in); ok != tc.ok {
			t.Errorf("parseSemver(%q) ok=%v, want %v", tc.in, ok, tc.ok)
		}
	}
}
