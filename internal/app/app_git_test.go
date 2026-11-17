package app

import (
	"context"
	"encoding/json"
	"fmt"
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
  "paths": [%q]
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

	if err := f.app.Add(ctx, f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg")); err != nil {
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
	if err := f.app.Refetch(ctx, nil); err != nil {
		t.Fatal(err, f.log)
	}
	if n := gh.Requests["codeload"]; n != 2 {
		t.Errorf("codeload requests after refetch = %d, want 2", n)
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
