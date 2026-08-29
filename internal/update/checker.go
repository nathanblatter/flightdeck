// Package update watches GitHub for a newer tagged release than the running
// build and exposes it as a one-line upgrade notice. The check is best-effort
// in the veccache spirit: any failure keeps the previous answer and never
// affects serving. Releases (not raw tags) are what count as "available" —
// the release workflow only publishes one after the test gates pass.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultRepo is the canonical source repo instances update from.
const DefaultRepo = "nathanblatter/flightdeck"

const checkInterval = 6 * time.Hour

// Release is the newest published release seen on GitHub.
type Release struct {
	Tag string // e.g. "v0.2.0"
	URL string // html_url of the release (notes/changelog)
}

// Checker polls the GitHub releases API and answers "is a newer version out".
// A nil Checker is valid and reports no update, so callers never guard.
type Checker struct {
	version string // build version of the running binary
	repo    string // "owner/name"
	token   string // optional, raises the API rate limit
	apiBase string // overridable in tests
	client  *http.Client
	latest  atomic.Pointer[Release]
}

// New builds a checker for the running version. The source repo comes from
// FLIGHTDECK_UPDATE_REPO (default DefaultRepo); setting it to "off" disables
// checking entirely, in which case New returns nil. FLIGHTDECK_GITHUB_TOKEN is
// honored if set (not needed for a public repo at 4 calls/day).
func New(version string) *Checker {
	repo := os.Getenv("FLIGHTDECK_UPDATE_REPO")
	if repo == "" {
		repo = DefaultRepo
	}
	if repo == "off" {
		return nil
	}
	apiBase := os.Getenv("FLIGHTDECK_UPDATE_API") // test/debug override
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	return &Checker{
		version: version,
		repo:    repo,
		token:   os.Getenv("FLIGHTDECK_GITHUB_TOKEN"),
		apiBase: apiBase,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Run checks once at boot and then every checkInterval until ctx is canceled.
func (c *Checker) Run(ctx context.Context) {
	if c == nil {
		return
	}
	c.checkAndLog(ctx)
	t := time.NewTicker(checkInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.checkAndLog(ctx)
		}
	}
}

func (c *Checker) checkAndLog(ctx context.Context) {
	if err := c.CheckNow(ctx); err != nil {
		log.Printf("update check (%s): %v", c.repo, err)
		return
	}
	if rel, ok := c.Available(); ok {
		log.Printf("update available: %s (running %s) — %s", rel.Tag, c.version, rel.URL)
	}
}

// CheckNow fetches the latest release and stores the snapshot. A 404 (no
// releases published yet) clears the snapshot rather than erroring.
func (c *Checker) CheckNow(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/repos/"+c.repo+"/releases/latest", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		c.latest.Store(nil)
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github api: %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return err
	}
	if body.TagName == "" {
		return fmt.Errorf("github api: release without tag_name")
	}
	c.latest.Store(&Release{Tag: body.TagName, URL: body.HTMLURL})
	return nil
}

// Available reports the latest release iff it is strictly newer than the
// running version. Unparsable versions on either side (a "dev"/"docker" build,
// a non-semver tag) report no update — an instance that can't be compared
// should never nag.
func (c *Checker) Available() (Release, bool) {
	if c == nil {
		return Release{}, false
	}
	rel := c.latest.Load()
	if rel == nil {
		return Release{}, false
	}
	cur, okCur := parseSemver(c.version)
	lat, okLat := parseSemver(rel.Tag)
	if !okCur || !okLat || !lat.newerThan(cur) {
		return Release{}, false
	}
	return *rel, true
}

// Notice returns the one-line upgrade prompt surfaced to agents, or "" when
// the instance is current (or can't tell).
func (c *Checker) Notice() string {
	rel, ok := c.Available()
	if !ok {
		return ""
	}
	n := fmt.Sprintf("flightdeck %s is available (running %s). To upgrade, run `flightdeck update` in the flightdeck repo checkout on the host machine.", rel.Tag, c.version)
	if rel.URL != "" {
		n += " Release notes: " + rel.URL
	}
	return n
}

// LatestTag returns the highest semver tag among tags (ignoring anything that
// doesn't parse as vX.Y.Z). Used by `flightdeck update` to pick the release to
// check out after fetching.
func LatestTag(tags []string) (string, bool) {
	var (
		best    semver
		bestTag string
		found   bool
	)
	for _, t := range tags {
		v, ok := parseSemver(t)
		if !ok {
			continue
		}
		if !found || v.newerThan(best) {
			best, bestTag, found = v, t, true
		}
	}
	return bestTag, found
}

// SameVersion reports whether two version strings share the same semver base
// (git-describe suffixes ignored). False when either side doesn't parse.
func SameVersion(a, b string) bool {
	va, okA := parseSemver(a)
	vb, okB := parseSemver(b)
	return okA && okB && va == vb
}

type semver struct{ major, minor, patch int }

func (a semver) newerThan(b semver) bool {
	if a.major != b.major {
		return a.major > b.major
	}
	if a.minor != b.minor {
		return a.minor > b.minor
	}
	return a.patch > b.patch
}

// parseSemver reads "vX.Y.Z", tolerating a git-describe suffix
// ("v1.2.3-4-gabc1234", "v1.2.3-dirty") by comparing the base tag only. A
// build that is N commits past the latest tag therefore compares equal to it
// and is not nagged.
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		out[i] = n
	}
	return semver{out[0], out[1], out[2]}, true
}
