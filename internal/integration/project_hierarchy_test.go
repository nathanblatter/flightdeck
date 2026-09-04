package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"flightdeck/internal/dto"
	"flightdeck/internal/service"
	"flightdeck/internal/store"
)

// mkChildProject creates a project nested under parent.
func mkChildProject(t testing.TB, st *store.Store, slug, parent string) store.Project {
	t.Helper()
	p, err := st.CreateProject(context.Background(), store.CreateProjectParams{
		Slug: slug, Name: slug, ParentSlug: &parent,
	})
	if err != nil {
		t.Fatalf("create child project %s under %s: %v", slug, parent, err)
	}
	return p
}

func TestProjectHierarchyValidation(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	mkProject(t, st, "root")
	mkChildProject(t, st, "mid", "root")
	mkChildProject(t, st, "leaf", "mid")

	if err := svc.ValidateProjectParent(ctx, "leaf", ""); err != nil {
		t.Fatalf("clearing a parent should always validate: %v", err)
	}
	if err := svc.ValidateProjectParent(ctx, "mid", "root"); err != nil {
		t.Fatalf("keeping a valid parent should validate: %v", err)
	}
	if err := svc.ValidateProjectParent(ctx, "root", "root"); !errors.Is(err, service.ErrInvalidProjectParent) {
		t.Fatalf("self-parent error = %v, want ErrInvalidProjectParent", err)
	}
	if err := svc.ValidateProjectParent(ctx, "root", "mid"); !errors.Is(err, service.ErrInvalidProjectParent) {
		t.Fatalf("direct cycle error = %v, want ErrInvalidProjectParent", err)
	}
	if err := svc.ValidateProjectParent(ctx, "root", "leaf"); !errors.Is(err, service.ErrInvalidProjectParent) {
		t.Fatalf("deep cycle error = %v, want ErrInvalidProjectParent", err)
	}
	if err := svc.ValidateProjectParent(ctx, "leaf", "missing"); !errors.Is(err, service.ErrNotFound) {
		t.Fatalf("unknown parent error = %v, want ErrNotFound", err)
	}
}

func TestProjectContextIncludesChildrenAndParent(t *testing.T) {
	st, svc := setup(t)
	ctx := context.Background()
	mkProject(t, st, "root")
	mkChildProject(t, st, "child-b", "root")
	mkChildProject(t, st, "child-a", "root")

	bundle, err := svc.ProjectContext(ctx, "root", service.VerbosityCompact)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Children) != 2 || bundle.Children[0].Slug != "child-a" || bundle.Children[1].Slug != "child-b" {
		t.Fatalf("children = %+v, want child-a then child-b", bundle.Children)
	}
	if bundle.Project.Parent != nil {
		t.Fatalf("root parent = %v, want nil", bundle.Project.Parent)
	}

	child, err := svc.ProjectContext(ctx, "child-a", service.VerbosityCompact)
	if err != nil {
		t.Fatal(err)
	}
	if child.Project.Parent == nil || *child.Project.Parent != "root" {
		t.Fatalf("child parent = %v, want root", child.Project.Parent)
	}
	if len(child.Children) != 0 {
		t.Fatalf("leaf children = %+v, want none", child.Children)
	}
}

func TestProjectHierarchyHTTPContract(t *testing.T) {
	ts, st, _, key := setupContextImpactHTTP(t)
	mkProject(t, st, "root")

	// Create nested; parent surfaces in the DTO.
	resp, body := doJSON(t, http.MethodPost, ts.URL+"/api/projects", key, map[string]any{
		"slug": "sub", "name": "Sub", "parent": "root",
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create nested: status %d: %s", resp.StatusCode, body)
	}
	var created dto.Project
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.Parent == nil || *created.Parent != "root" {
		t.Fatalf("created parent = %v, want root", created.Parent)
	}

	// Unknown parent 404s, cycle 400s.
	if resp, _ := doJSON(t, http.MethodPost, ts.URL+"/api/projects", key, map[string]any{
		"slug": "orphan", "name": "Orphan", "parent": "missing",
	}, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown parent status = %d, want 404", resp.StatusCode)
	}
	if resp, _ := doJSON(t, http.MethodPatch, ts.URL+"/api/projects/root", key, map[string]any{
		"parent": "sub",
	}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("cycle status = %d, want 400", resp.StatusCode)
	}

	// PATCH without parent leaves it untouched; "" clears it.
	resp, body = doJSON(t, http.MethodPatch, ts.URL+"/api/projects/sub", key, map[string]any{
		"name": "Sub renamed",
	}, nil)
	var patched dto.Project
	if err := json.Unmarshal(body, &patched); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("rename: status %d, err %v", resp.StatusCode, err)
	}
	if patched.Parent == nil || *patched.Parent != "root" {
		t.Fatalf("parent after unrelated patch = %v, want root", patched.Parent)
	}
	resp, body = doJSON(t, http.MethodPatch, ts.URL+"/api/projects/sub", key, map[string]any{
		"parent": "",
	}, nil)
	var cleared dto.Project
	if err := json.Unmarshal(body, &cleared); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("clear parent: status %d, err %v", resp.StatusCode, err)
	}
	if cleared.Parent != nil {
		t.Fatalf("parent after clear = %v, want nil", cleared.Parent)
	}
}

func TestProjectHierarchySlugRenameCascades(t *testing.T) {
	st, _ := setup(t)
	ctx := context.Background()
	mkProject(t, st, "root")
	mkChildProject(t, st, "sub", "root")

	// Slug renames don't go through the API today, but the FK must keep the
	// tree consistent if one ever happens (ON UPDATE CASCADE).
	if _, err := st.Pool.Exec(ctx, `UPDATE projects SET slug = 'root2' WHERE slug = 'root'`); err != nil {
		t.Fatal(err)
	}
	sub, err := st.GetProjectBySlug(ctx, "sub")
	if err != nil {
		t.Fatal(err)
	}
	if sub.ParentSlug == nil || *sub.ParentSlug != "root2" {
		t.Fatalf("parent after rename = %v, want root2", sub.ParentSlug)
	}
}
