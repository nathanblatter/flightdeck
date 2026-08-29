package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"flightdeck/internal/update"
)

// runUpdate upgrades an instance to the latest tagged release: fetch tags,
// check out the highest vX.Y.Z tag in the repo checkout, and re-apply the
// instance (compose regen + `docker compose up -d --build`). Migrations run on
// container boot; instance data lives in pgdata/ and is untouched. Flags match
// `up` so the invocation is the one users already know.
func runUpdate(args []string) {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	dir, name, port, repo := instanceFlags(fs)
	force := fs.Bool("force", false, "rebuild even when already on the latest release")
	_ = fs.Parse(args)

	o := resolveInstance(*dir, *name, *port, *repo)

	if out, err := exec.Command("git", "-C", o.repoAbs, "fetch", "--tags", "origin").CombinedOutput(); err != nil {
		log.Fatalf("git fetch: %v\n%s", err, out)
	}

	tagsOut, err := exec.Command("git", "-C", o.repoAbs, "tag", "--list", "v*").Output()
	if err != nil {
		log.Fatalf("git tag --list: %v", err)
	}
	latest, ok := update.LatestTag(strings.Fields(string(tagsOut)))
	if !ok {
		log.Fatalf("no release tags (vX.Y.Z) found in %s — nothing to update to", o.repoAbs)
	}

	current := repoVersion(o.repoAbs)
	if update.SameVersion(current, latest) && !*force {
		fmt.Printf("already on the latest release (%s); pass --force to rebuild anyway\n", latest)
		return
	}

	// Refuse to move a dirty checkout — the instance host shouldn't have local
	// edits, and silently discarding them is worse than stopping.
	if dirty, err := exec.Command("git", "-C", o.repoAbs, "status", "--porcelain").Output(); err != nil {
		log.Fatalf("git status: %v", err)
	} else if len(strings.TrimSpace(string(dirty))) > 0 {
		log.Fatalf("checkout %s has uncommitted changes — commit/stash them (or run `flightdeck up` to rebuild from the working tree as-is)", o.repoAbs)
	}

	if !update.SameVersion(current, latest) {
		if out, err := exec.Command("git", "-C", o.repoAbs, "checkout", latest).CombinedOutput(); err != nil {
			log.Fatalf("git checkout %s: %v\n%s", latest, err, out)
		}
	}

	fmt.Printf("updating instance %q: %s → %s\n", o.instName, current, latest)
	applyInstance(o, false)

	if err := waitHealthy(o.port, 60*time.Second); err != nil {
		log.Fatalf("instance did not become healthy: %v\ncheck logs: docker compose -p %s --project-directory %s logs flightdeck", err, o.instName, o.instAbs)
	}
	fmt.Printf("updated %s → %s (http://127.0.0.1:%d healthy)\n", current, latest, o.port)
}

// waitHealthy polls the instance /healthz until it answers 200 or the timeout
// elapses — the visible confirmation that the new build actually came up
// (compose reports containers *started*, not migrated and serving).
func waitHealthy(port int, timeout time.Duration) error {
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(timeout)
	for {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("timed out after %s (last error: %v)", timeout, err)
			}
			return fmt.Errorf("timed out after %s (last status: %s)", timeout, resp.Status)
		}
		time.Sleep(2 * time.Second)
	}
}
