package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaikoga/vpmify/internal/testutil"
)

// monorepo is a repository with two packages in lockstep, like UniVRM's
// com.vrmc.gltf and com.vrmc.univrm.
func monorepo(versions ...string) *testutil.FakeGitHub {
	gh := testutil.NewFakeGitHub("owner", "mono")
	for _, v := range versions {
		gh.Tag("v"+v, map[string]string{
			"README.md":                  "root",
			"Packages/Base/package.json": fmt.Sprintf(`{"name": "com.example.base", "version": %q, "unity": "2021.3"}`, v),
			"Packages/Base/Base.cs":      "class Base {}",
			"Packages/Pkg/package.json": fmt.Sprintf(`{
  "name": "com.example.pkg",
  "version": %q,
  "unity": "2021.3",
  "dependencies": {
    "com.example.base": %q,
    "com.unity.ugui": "1.0.0",
    "org.other.lib": "1.0.0"
  }
}
`, v, v),
			"Packages/Pkg/Pkg.cs": "class Pkg {}",
		})
	}
	return gh
}

func (f *fixture) writeGitRecipe(id, name, dir string) string {
	f.t.Helper()
	content := fmt.Sprintf(`{
  "id": %q,
  "source": {"type": "git", "url": "https://github.com/owner/mono.git", "name": %q, "tagPattern": "v*"},
  "path": %q
}`, id, name, dir)
	p := filepath.Join(f.t.TempDir(), id+".json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func TestGitRecipesShareArchivesAndPromoteDependencies(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()

	if err := f.addAndPull(PullOptions{}, f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg")); err != nil {
		t.Fatal(err, f.log)
	}
	pkgDir := f.app.Layout.PackageDir("com.example.pkg", "1.1.0")
	m := readManifest(t, pkgDir)
	if _, ok := m["vpmDependencies"]; ok {
		t.Errorf("vpmDependencies before base has a recipe: %v", m["vpmDependencies"])
	}
	log := f.log.String()
	if !strings.Contains(log, "depends on com.example.base, which no recipe provides") ||
		!strings.Contains(log, "depends on org.other.lib, which no recipe provides") ||
		strings.Contains(log, "com.unity.ugui, which") {
		t.Errorf("notes in log:\n%s", log)
	}

	if err := f.app.Add(ctx, f.writeGitRecipe("base", "com.example.base", "Packages/Base")); err != nil {
		t.Fatal(err, f.log)
	}
	if m = readManifest(t, pkgDir); m["vpmDependencies"] != nil {
		t.Errorf("add rebuilt pkg: %v", m["vpmDependencies"])
	}
	// Pulling only base builds pkg again too.
	if err := f.app.Pull(ctx, []string{"base"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	m = readManifest(t, pkgDir)
	if got, want := m["vpmDependencies"], map[string]any{"com.example.base": ">=1.1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("vpmDependencies = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "Pkg.cs")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "Base.cs")); !os.IsNotExist(err) {
		t.Errorf("Base.cs in pkg: %v", err)
	}
	if got, want := f.placed(), []string{"1.1.0@2021.3", "1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
	// One archive for the v1.1.0 commit serves both recipes and the rebuild.
	if n := gh.Requests["codeload"]; n != 1 {
		t.Errorf("codeload requests = %d, want 1", n)
	}

	// Refetching downloads the shared archive once more, not once per recipe.
	if err := f.app.Pull(ctx, nil, PullOptions{Refetch: true}); err != nil {
		t.Fatal(err, f.log)
	}
	if n := gh.Requests["codeload"]; n != 2 {
		t.Errorf("codeload requests after refetch = %d, want 2", n)
	}
}

// Pulling only the dependent after adding its dependency's recipe places
// the dependency too: its vpmDependencies would point at nothing otherwise.
func TestPullPlacesTheDependencyItWrites(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{}, f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg")); err != nil {
		t.Fatal(err, f.log)
	}
	// An older pkg too, so its dependency is not the newest base.
	if err := f.app.Pull(ctx, []string{"pkg@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if err := f.app.Add(ctx, f.writeGitRecipe("base", "com.example.base", "Packages/Base")); err != nil {
		t.Fatal(err, f.log)
	}
	if err := f.app.Pull(ctx, []string{"pkg"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	m := readManifest(t, f.app.Layout.PackageDir("com.example.pkg", "1.0.0"))
	if got, want := m["vpmDependencies"], map[string]any{"com.example.base": ">=1.0.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("vpmDependencies = %v, want %v", got, want)
	}
	// The versions the dependents name, not only the newest.
	if got, want := f.placed(), []string{"1.0.0@2021.3", "1.1.0@2021.3", "1.0.0@2021.3", "1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
}

func TestPullFailsWhenTheDependencyCannotBePlaced(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{}, f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg")); err != nil {
		t.Fatal(err, f.log)
	}
	// base's recipe reads only tags before pkg's version.
	content := `{"id": "base", "source": {"type": "git", "url": "https://github.com/owner/mono.git", "name": "com.example.base", "tagPattern": "v*", "tags": "<1.1.0"},
  "path": "Packages/Base"}`
	p := filepath.Join(t.TempDir(), "base.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Add(ctx, p); err != nil {
		t.Fatal(err, f.log)
	}
	err := f.app.Pull(ctx, []string{"pkg"}, PullOptions{})
	if err == nil || !strings.Contains(err.Error(), "com.example.pkg 1.1.0 needs com.example.base >=1.1.0") {
		t.Errorf("err = %v", err)
	}
}

type failCodeload struct{ next http.RoundTripper }

func (f failCodeload) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Host, "codeload") {
		return nil, errors.New("network down")
	}
	return f.next.RoundTrip(req)
}

// A rebuild of a recipe the pull does not name fails the pull.
func TestPullFailsWhenAnUnnamedRebuildFails(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{},
		f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg"),
		f.writeGitRecipe("base", "com.example.base", "Packages/Base")); err != nil {
		t.Fatal(err, f.log)
	}
	// New overrides keep the version list but change how pkg is built.
	content := `{"id": "pkg", "source": {"type": "git", "url": "https://github.com/owner/mono.git", "name": "com.example.pkg", "tagPattern": "v*"},
  "path": "Packages/Pkg", "overrides": [{"set": {"displayName": "X"}}]}`
	if err := os.WriteFile(f.app.recipePath("pkg"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(f.app.Layout.Cache()); err != nil {
		t.Fatal(err)
	}
	f.app.Client.HTTP.Transport = failCodeload{gh}
	err := f.app.Pull(ctx, []string{"base"}, PullOptions{})
	if err == nil || !strings.Contains(err.Error(), "pkg: com.example.pkg 1.1.0") {
		t.Errorf("err = %v", err)
	}
}

func readManifest(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
