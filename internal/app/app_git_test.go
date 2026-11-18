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

// pkgNeedsBase makes pkg depend on base in vpmDependencies, as a recipe
// has to say for itself.
const pkgNeedsBase = `,
  "overrides": [{"set": {"vpmDependencies": {"com.example.base": ">={dependencies}"}}}]`

func (f *fixture) writeGitRecipe(id, name, dir, extra string) string {
	f.t.Helper()
	content := fmt.Sprintf(`{
  "id": %q,
  "source": {"type": "github", "url": "https://github.com/owner/mono.git", "name": %q, "tagPattern": "v*"},
  "path": %q%s
}`, id, name, dir, extra)
	p := filepath.Join(f.t.TempDir(), id+".json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func TestGitRecipesShareArchivesAndWriteTheDependenciesTheyName(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()

	if err := f.addAndPull(PullOptions{},
		f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg", pkgNeedsBase),
		f.writeGitRecipe("base", "com.example.base", "Packages/Base", "")); err != nil {
		t.Fatal(err, f.log)
	}
	pkgDir := f.app.Layout.PackageDir("com.example.pkg", "1.1.0")
	m := readManifest(t, pkgDir)
	if got, want := m["vpmDependencies"], map[string]any{"com.example.base": ">=1.1.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("vpmDependencies = %v, want %v", got, want)
	}
	// The UPM dependencies the recipe does not name stay out, with a note;
	// Unity's own packages without one.
	log := f.log.String()
	if !strings.Contains(log, "upstream com.example.pkg depends on org.other.lib, which the recipe does not write") ||
		strings.Contains(log, "on com.example.base, which") || strings.Contains(log, "com.unity.ugui, which") {
		t.Errorf("notes in log:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "Pkg.cs")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "Base.cs")); !os.IsNotExist(err) {
		t.Errorf("Base.cs in pkg: %v", err)
	}
	if got, want := f.available(), []string{"1.1.0@2021.3", "1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("available = %v, want %v", got, want)
	}
	// One archive for the v1.1.0 commit serves both recipes.
	if n := gh.Requests["codeload"]; n != 1 {
		t.Errorf("codeload requests = %d, want 1", n)
	}

	// Refetching downloads the shared archive once more, not once per recipe.
	if _, err := f.app.Pull(ctx, nil, PullOptions{Refetch: true}); err != nil {
		t.Fatal(err, f.log)
	}
	if n := gh.Requests["codeload"]; n != 2 {
		t.Errorf("codeload requests after refetch = %d, want 2", n)
	}
}

// What a recipe builds depends on the recipe alone: no UPM dependency is
// written for it, and other recipes coming and going rebuild nothing.
func TestARecipeBuildsTheSameWhateverOtherRecipesThereAre(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	if err := f.addAndPull(PullOptions{}, f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg", "")); err != nil {
		t.Fatal(err, f.log)
	}
	// base's recipe comes later, and the pull makes base available.
	if err := f.addAndPull(PullOptions{}, f.writeGitRecipe("base", "com.example.base", "Packages/Base", "")); err != nil {
		t.Fatal(err, f.log)
	}
	if m := readManifest(t, f.app.Layout.PackageDir("com.example.pkg", "1.1.0")); m["vpmDependencies"] != nil {
		t.Errorf("vpmDependencies = %v", m["vpmDependencies"])
	}
	if n := strings.Count(f.log.String(), "converting com.example.pkg"); n != 1 {
		t.Errorf("pkg converted %d times:\n%s", n, f.log)
	}
}

// Pulling only the dependent makes the versions of the dependency it names
// available too: its vpmDependencies would point at nothing otherwise.
func TestPullAddsTheDependencyItWrites(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	if _, err := f.app.Add(ctx,
		f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg", pkgNeedsBase),
		f.writeGitRecipe("base", "com.example.base", "Packages/Base", "")); err != nil {
		t.Fatal(err, f.log)
	}
	// An older pkg too, so its dependency is not the newest base.
	res, err := f.app.Pull(ctx, []string{"pkg", "pkg@1.0.0"}, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	var built []string
	for _, b := range res.Built {
		built = append(built, fmt.Sprintf("%s %s %v", b.Package, b.Version, b.Dependency))
	}
	if want := []string{"com.example.pkg 1.1.0 false", "com.example.pkg 1.0.0 false", "com.example.base 1.0.0 true", "com.example.base 1.1.0 true"}; !reflect.DeepEqual(built, want) {
		t.Errorf("built = %v, want %v", built, want)
	}
	m := readManifest(t, f.app.Layout.PackageDir("com.example.pkg", "1.0.0"))
	if got, want := m["vpmDependencies"], map[string]any{"com.example.base": ">=1.0.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("vpmDependencies = %v, want %v", got, want)
	}
	// The versions the dependents name, not only the newest.
	if got, want := f.available(), []string{"1.0.0@2021.3", "1.1.0@2021.3", "1.0.0@2021.3", "1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("available = %v, want %v", got, want)
	}
}

func TestPullFailsWhenTheDependencyCannotBeAdded(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	// base's recipe reads only tags before pkg's version.
	content := `{"id": "base", "source": {"type": "github", "url": "https://github.com/owner/mono.git", "name": "com.example.base", "tagPattern": "v*", "tags": "<1.1.0"},
  "path": "Packages/Base"}`
	p := filepath.Join(t.TempDir(), "base.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Add(ctx, f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg", pkgNeedsBase), p); err != nil {
		t.Fatal(err, f.log)
	}
	_, err := f.app.Pull(ctx, []string{"pkg"}, PullOptions{})
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

// A recipe changed since its versions were built builds them again when it
// is pulled, and only then; a failure to build fails the pull.
func TestPullBuildsAgainOnlyTheRecipesItPulls(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{},
		f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg", ""),
		f.writeGitRecipe("base", "com.example.base", "Packages/Base", "")); err != nil {
		t.Fatal(err, f.log)
	}
	// New overrides keep the version list but change how pkg is built.
	content := `{"id": "pkg", "source": {"type": "github", "url": "https://github.com/owner/mono.git", "name": "com.example.pkg", "tagPattern": "v*"},
  "path": "Packages/Pkg", "overrides": [{"set": {"displayName": "X"}}]}`
	if err := os.WriteFile(f.app.recipePath("pkg"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgDir := f.app.Layout.PackageDir("com.example.pkg", "1.1.0")
	if _, err := f.app.Pull(ctx, []string{"base"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if m := readManifest(t, pkgDir); m["displayName"] != nil {
		t.Errorf("pulling base built pkg again: %v", m)
	}

	if err := os.RemoveAll(f.app.Layout.Cache()); err != nil {
		t.Fatal(err)
	}
	f.app.Client.HTTP.Transport = failCodeload{gh}
	if _, err := f.app.Pull(ctx, []string{"pkg"}, PullOptions{}); err == nil || !strings.Contains(err.Error(), "pkg: com.example.pkg 1.1.0") {
		t.Errorf("err = %v", err)
	}

	f.app.Client.HTTP.Transport = gh
	if _, err := f.app.Pull(ctx, []string{"pkg"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if m := readManifest(t, pkgDir); m["displayName"] != "X" {
		t.Errorf("pkg not built again: %v", m)
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
