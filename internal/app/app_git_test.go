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

const monoURL = "https://github.com/owner/mono.git"

// monorepo is a repository with two packages in lockstep, like UniVRM's
// com.vrmc.gltf and com.vrmc.univrm.
func monorepo(versions ...string) *testutil.FakeGitHub {
	gh := testutil.NewFakeGitHub("owner", "mono")
	for _, v := range versions {
		gh.Tag("v"+v, monoTag(v, v))
	}
	return gh
}

// monoTag is the files of a tag in which base has version base and pkg
// version pkg.
func monoTag(base, pkg string) map[string]string {
	return map[string]string{
		"README.md":                  "root",
		"Packages/Base/package.json": fmt.Sprintf(`{"name": "com.example.base", "version": %q, "unity": "2021.3"}`, base),
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
`, pkg, base),
		"Packages/Pkg/Pkg.cs": "class Pkg {}",
	}
}

// pkgNeedsBase makes pkg depend on base in vpmDependencies, as a recipe
// has to say for itself.
const pkgNeedsBase = `,
  "overrides": [{"set": {"vpmDependencies": {"com.example.base": ">={dependencies}"}}}]`

// gitRecipe returns a recipe with one source on the monorepo, whose
// packages are given as JSON objects.
func gitRecipe(id, sourceExtra string, packages ...string) string {
	return fmt.Sprintf(`{
  "vpmifyFormat": 1, "displayName": "", "id": %q,
  "sources": [{"type": "github", "url": %q%s, "packages": [%s]}]
}`, id, monoURL, sourceExtra, strings.Join(packages, ", "))
}

func gitPackage(name, dir, extra string) string {
	return fmt.Sprintf(`{"name": %q, "path": %q%s}`, name, dir, extra)
}

func (f *fixture) writeFile(name, content string) string {
	f.t.Helper()
	p := filepath.Join(f.t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *fixture) writeGitRecipe(id, name, dir, extra string) string {
	f.t.Helper()
	return f.writeFile(id+".json", gitRecipe(id, "", gitPackage(name, dir, extra)))
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
	pkgDir := f.app.Layout.PackageDir("pkg", "com.example.pkg", "1.1.0")
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
	if m := readManifest(t, f.app.Layout.PackageDir("pkg", "com.example.pkg", "1.1.0")); m["vpmDependencies"] != nil {
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
	// The dependencies come in the order of the versions that need them.
	if want := []string{"com.example.pkg 1.1.0 false", "com.example.pkg 1.0.0 false", "com.example.base 1.1.0 true", "com.example.base 1.0.0 true"}; !reflect.DeepEqual(built, want) {
		t.Errorf("built = %v, want %v", built, want)
	}
	m := readManifest(t, f.app.Layout.PackageDir("pkg", "com.example.pkg", "1.0.0"))
	if got, want := m["vpmDependencies"], map[string]any{"com.example.base": ">=1.0.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("vpmDependencies = %v, want %v", got, want)
	}
	// The versions the dependents name, not only the newest.
	if got, want := f.available(), []string{"1.0.0@2021.3", "1.1.0@2021.3", "1.0.0@2021.3", "1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("available = %v, want %v", got, want)
	}
}

// A recipe that provides the dependency too but has no version list is
// passed over for the next, and the log says it in the front end's words,
// which may name what mends it.
func TestPullPassesOverAProviderWithoutAList(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	f.app.LogError = func(err error) string { return err.Error() + "; mended so" }
	ctx := context.Background()
	if _, err := f.app.Add(ctx,
		f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg", pkgNeedsBase),
		f.writeGitRecipe("abase", "com.example.base", "Packages/Base", ""),
		f.writeGitRecipe("base", "com.example.base", "Packages/Base", "")); err != nil {
		t.Fatal(err, f.log)
	}
	if err := os.Remove(f.app.Layout.Index("abase")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Pull(ctx, []string{"pkg"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if want := "abase: passed over for com.example.base >=1.1.0: no usable version list: none yet; mended so\n"; !strings.Contains(f.log.String(), want) {
		t.Errorf("log = %s, want %q", f.log, want)
	}
}

// An onDemand package is not chosen by a pull that names no package, with
// --all either: it comes as a dependency, or when the user names it or a
// version.
func TestPullLeavesOnDemandPackagesToDependenciesAndNames(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	pull := func(opt PullOptions, args ...string) []string {
		t.Helper()
		res, err := f.app.Pull(ctx, args, opt)
		if err != nil {
			t.Fatal(err, f.log)
		}
		built := []string{}
		for _, b := range res.Built {
			built = append(built, fmt.Sprintf("%s %s %s %v", b.Recipe, b.Package, b.Version, b.Dependency))
		}
		return built
	}
	base := gitPackage("com.example.base", "Packages/Base", `, "onDemand": true`)
	if _, err := f.app.Add(ctx,
		f.writeFile("mono.json", gitRecipe("mono", "", gitPackage("com.example.pkg", "Packages/Pkg", ""), base)),
		f.writeFile("dep.json", gitRecipe("dep", "", gitPackage("com.example.pkg", "Packages/Pkg", pkgNeedsBase), base))); err != nil {
		t.Fatal(err, f.log)
	}
	// The version lists say which packages are onDemand, for the GUI to
	// know what a pull that names no package chooses from.
	list, err := f.app.Versions("mono", VersionsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	onDemand := map[string]bool{}
	for _, p := range list.Packages {
		onDemand[p.Package] = p.OnDemand
	}
	if want := map[string]bool{"com.example.pkg": false, "com.example.base": true}; !reflect.DeepEqual(onDemand, want) {
		t.Errorf("onDemand = %v, want %v", onDemand, want)
	}

	// Nothing depends on base in mono: it is left, with --all too.
	if got, want := pull(PullOptions{All: true}, "mono"), []string{"mono com.example.pkg 1.0.0 false", "mono com.example.pkg 1.1.0 false"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pull --all mono built %v, want %v", got, want)
	}
	// In dep the newest pkg depends on base, which comes as its dependency
	// while no recipe has made it available.
	if got, want := pull(PullOptions{}, "dep"), []string{"dep com.example.pkg 1.1.0 false", "dep com.example.base 1.1.0 true"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pull dep built %v, want %v", got, want)
	}
	// Naming the package chooses from it, and a version named for the recipe
	// takes every package that has it.
	if got, want := pull(PullOptions{}, "mono/com.example.base"), []string{"mono com.example.base 1.1.0 false"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pull mono/com.example.base built %v, want %v", got, want)
	}
	if got, want := pull(PullOptions{}, "mono@1.0.0"), []string{"mono com.example.base 1.0.0 false"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pull mono@1.0.0 built %v, want %v", got, want)
	}
}

func TestPullFailsWhenTheDependencyCannotBeAdded(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	// base's recipe reads only tags before pkg's version.
	p := f.writeFile("base.json", gitRecipe("base", `, "tags": "<1.1.0"`, gitPackage("com.example.base", "Packages/Base", "")))
	if _, err := f.app.Add(ctx, f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg", pkgNeedsBase), p); err != nil {
		t.Fatal(err, f.log)
	}
	_, err := f.app.Pull(ctx, []string{"pkg"}, PullOptions{})
	if err == nil || !strings.Contains(err.Error(), "com.example.pkg 1.1.0 needs com.example.base >=1.1.0") {
		t.Errorf("err = %v", err)
	}
	var noMatch *NoMatchError
	if !errors.As(err, &noMatch) || *noMatch != (NoMatchError{Recipe: "base", Package: "com.example.base", Range: ">=1.1.0", Dependent: "com.example.pkg 1.1.0"}) {
		t.Errorf("no match = %+v", noMatch)
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
	content := gitRecipe("pkg", "", gitPackage("com.example.pkg", "Packages/Pkg", `, "overrides": [{"set": {"displayName": "X"}}]`))
	if err := os.WriteFile(f.app.recipePath("pkg"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	pkgDir := f.app.Layout.PackageDir("pkg", "com.example.pkg", "1.1.0")
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
	if _, err := f.app.Pull(ctx, []string{"pkg"}, PullOptions{}); err == nil || !strings.Contains(err.Error(), "pkg: com.example.pkg: 1.1.0") {
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

// routeByRepo sends each request for a GitHub repository to its fake.
type routeByRepo []*testutil.FakeGitHub

func (r routeByRepo) RoundTrip(req *http.Request) (*http.Response, error) {
	for _, g := range r {
		p := "/" + g.Owner + "/" + g.Repo
		if strings.HasPrefix(req.URL.Path, p+"/") || strings.HasPrefix(req.URL.Path, p+".git/") {
			return g.RoundTrip(req)
		}
	}
	return nil, fmt.Errorf("no fake for %s", req.URL)
}

// overlayRepos returns a monorepo whose pkg keeps a submodule at Sub, as
// UniVRM keeps MToon, and the repository the submodule comes from, with
// the commits of its tags s1 and s2.
func overlayRepos() (mono, sub *testutil.FakeGitHub, s1, s2 string) {
	mono = testutil.NewFakeGitHub("owner", "mono")
	files := monoTag("1.0.0", "1.0.0")
	files["Packages/Pkg/Sub/"] = ""
	mono.Tag("v1.0.0", files)
	sub = testutil.NewFakeGitHub("owner", "sub")
	s1 = sub.Tag("s1", map[string]string{"Shader/Toon.shader": "one", "README.md": "sub"})
	s2 = sub.Tag("s2", map[string]string{"Shader/Toon.shader": "two"})
	return mono, sub, s1, s2
}

func overlayRecipe(path, commit string) string {
	return gitRecipe("pkg", "", gitPackage("com.example.pkg", "Packages/Pkg",
		fmt.Sprintf(`, "overlays": [{"tags": ">=1.0.0", "path": %q, "url": "https://github.com/owner/sub", "commit": %q}]`, path, commit)))
}

func TestOverlayFillsTheSubmoduleFolder(t *testing.T) {
	f := newFixture(t)
	mono, sub, s1, s2 := overlayRepos()
	f.app.Client.HTTP.Transport = routeByRepo{mono, sub}
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{}, f.writeFile("pkg.json", overlayRecipe("Sub", s1))); err != nil {
		t.Fatal(err, f.log)
	}
	pkgDir := f.app.Layout.PackageDir("pkg", "com.example.pkg", "1.0.0")
	read := func() string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(pkgDir, "Sub", "Shader", "Toon.shader"))
		if err != nil {
			t.Fatal(err, f.log)
		}
		return string(data)
	}
	if got := read(); got != "one" {
		t.Errorf("Toon.shader = %q", got)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "Pkg.cs")); err != nil {
		t.Error(err)
	}
	if n := sub.Requests["codeload"]; n != 1 {
		t.Errorf("sub codeload requests = %d, want 1", n)
	}

	// Another commit needs an update, as other changes to what update reads
	// do, and then builds the version again.
	if err := os.WriteFile(f.app.recipePath("pkg"), []byte(overlayRecipe("Sub", s2)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Pull(ctx, nil, PullOptions{}); noListRecipe(err) != "pkg" {
		t.Errorf("pull before update: err = %v", err)
	}
	if _, err := f.app.Update(ctx, nil); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.Pull(ctx, nil, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got := read(); got != "two" {
		t.Errorf("Toon.shader after the commit changed = %q", got)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "Sub", "README.md")); !os.IsNotExist(err) {
		t.Errorf("README.md of the old commit: %v", err)
	}

	// Refetching downloads the overlay's archive again too.
	if _, err := f.app.Pull(ctx, nil, PullOptions{Refetch: true}); err != nil {
		t.Fatal(err, f.log)
	}
	if n := sub.Requests["codeload"]; n != 3 {
		t.Errorf("sub codeload requests after refetch = %d, want 3", n)
	}
}

func TestOverlayNeedsAnEmptyFolder(t *testing.T) {
	cases := []struct {
		name, path string
		files      map[string]string // added to the package's tag
	}{
		{"missing", "Missing", nil},
		{"a file", "Pkg.cs", nil},
		{"not empty", "Sub", map[string]string{"Packages/Pkg/Sub/Kept.cs": "class Kept {}"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			_, sub, s1, _ := overlayRepos()
			files := monoTag("1.0.0", "1.0.0")
			files["Packages/Pkg/Sub/"] = ""
			for p, content := range c.files {
				files[p] = content
			}
			mono := testutil.NewFakeGitHub("owner", "mono")
			mono.Tag("v1.0.0", files)
			f.app.Client.HTTP.Transport = routeByRepo{mono, sub}
			ctx := context.Background()
			if _, err := f.app.Add(ctx, f.writeFile("pkg.json", overlayRecipe(c.path, s1))); err != nil {
				t.Fatal(err, f.log)
			}
			// A pull that names no version passes it over with a warning.
			res, err := f.app.Pull(ctx, []string{"pkg/com.example.pkg"}, PullOptions{})
			if err != nil {
				t.Fatal(err, f.log)
			}
			if len(res.Warnings) != 1 || res.Warnings[0].Recipe != "pkg" || !strings.Contains(res.Warnings[0].Warning, "has no empty folder") {
				t.Errorf("warnings = %v", res.Warnings)
			}
			if len(res.UpToDate) != 0 {
				t.Errorf("up to date = %v", res.UpToDate)
			}
			// A version named outright fails.
			if _, err := f.app.Pull(ctx, []string{"pkg/com.example.pkg@1.0.0"}, PullOptions{}); err == nil || !strings.Contains(err.Error(), "has no empty folder") {
				t.Errorf("named: err = %v", err)
			}
			if _, err := os.Stat(f.app.Layout.PackageDir("pkg", "com.example.pkg", "1.0.0")); !os.IsNotExist(err) {
				t.Errorf("the version was left: %v", err)
			}
		})
	}
}

// The versions that can be converted are made available beside one passed
// over, and the newest is not replaced with an older one.
func TestOverlayFolderPassesOverOneVersion(t *testing.T) {
	_, sub, s1, _ := overlayRepos()
	mono := testutil.NewFakeGitHub("owner", "mono")
	good := monoTag("1.0.0", "1.0.0")
	good["Packages/Pkg/Sub/"] = ""
	mono.Tag("v1.0.0", good)
	bad := monoTag("1.1.0", "1.1.0")
	bad["Packages/Pkg/Sub/Kept.cs"] = "class Kept {}"
	mono.Tag("v1.1.0", bad)
	recipe := overlayRecipe("Sub", s1)
	ctx := context.Background()

	f := newFixture(t)
	f.app.Client.HTTP.Transport = routeByRepo{mono, sub}
	if _, err := f.app.Add(ctx, f.writeFile("pkg.json", recipe)); err != nil {
		t.Fatal(err, f.log)
	}
	res, err := f.app.Pull(ctx, []string{"pkg/com.example.pkg"}, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(res.Built) != 0 || len(res.Warnings) != 1 {
		t.Errorf("newest: built = %v, warnings = %v", res.Built, res.Warnings)
	}
	res, err = f.app.Pull(ctx, []string{"pkg/com.example.pkg"}, PullOptions{All: true})
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(res.Built) != 1 || res.Built[0].Version != "1.0.0" || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Warning, "com.example.pkg 1.1.0") {
		t.Errorf("all: built = %v, warnings = %v", res.Built, res.Warnings)
	}
}

// sameTagRepo has base numbered apart from the tags, as UniVRM's UniGLTF
// was, and a tag without base.
func sameTagRepo() *testutil.FakeGitHub {
	gh := testutil.NewFakeGitHub("owner", "mono")
	gh.Tag("v1.0.0", monoTag("2.16.0", "1.0.0"))
	gh.Tag("v1.1.0", monoTag("2.17.0", "1.1.0"))
	noBase := monoTag("0.0.0", "1.2.0")
	delete(noBase, "Packages/Base/package.json")
	gh.Tag("v1.2.0", noBase)
	return gh
}

// pkgSameTag is a recipe whose pkg depends on the base of its own tag.
var pkgSameTag = gitRecipe("mono", "",
	gitPackage("com.example.base", "Packages/Base", ""),
	gitPackage("com.example.pkg", "Packages/Pkg", `, "overrides": [{"set": {"vpmDependencies": {"com.example.base": "={sameTag}"}}}]`))

func TestSameTag(t *testing.T) {
	f := newFixture(t)
	gh := sameTagRepo()
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	if _, err := f.app.Add(ctx, f.writeFile("mono.json", pkgSameTag)); err != nil {
		t.Fatal(err, f.log)
	}
	// One listing of the tags reads both packages.
	if n := gh.Requests["refs"]; n != 1 {
		t.Errorf("refs requests = %d, want 1", n)
	}

	// pkg 1.2.0 has no base in its tag, so it cannot be converted.
	list, err := f.app.Versions("mono/com.example.pkg", VersionsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var shown []string
	for _, v := range list.Packages[0].Versions {
		shown = append(shown, fmt.Sprintf("%s unusable=%v", v.Version, v.Unusable != ""))
	}
	if want := []string{"1.2.0 unusable=true", "1.1.0 unusable=false", "1.0.0 unusable=false"}; !reflect.DeepEqual(shown, want) {
		t.Errorf("versions = %v, want %v", shown, want)
	}
	if _, err := f.app.Pull(ctx, []string{"mono/com.example.pkg@1.2.0"}, PullOptions{}); err == nil || !strings.Contains(err.Error(), "same tag has no version of com.example.base") {
		t.Errorf("pull 1.2.0: err = %v", err)
	}

	// Naming only an old pkg brings the base of its tag, from the recipe.
	res, err := f.app.Pull(ctx, []string{"mono/com.example.pkg@1.0.0"}, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	var built []string
	for _, b := range res.Built {
		built = append(built, fmt.Sprintf("%s %s %v", b.Package, b.Version, b.Dependency))
	}
	// The named version alone is added, and the other package is only made
	// to satisfy it.
	if want := []string{"com.example.pkg 1.0.0 false", "com.example.base 2.16.0 true"}; !reflect.DeepEqual(built, want) {
		t.Errorf("built = %v, want %v", built, want)
	}
	m := readManifest(t, f.app.Layout.PackageDir("mono", "com.example.pkg", "1.0.0"))
	if got, want := m["vpmDependencies"], map[string]any{"com.example.base": "=2.16.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("vpmDependencies = %v, want %v", got, want)
	}
}

// A stable pkg first seen in a release candidate's tag depends on the
// release's base, not the candidate's prerelease.
func TestSameTagOfAStableVersionIsTheRelease(t *testing.T) {
	f := newFixture(t)
	gh := testutil.NewFakeGitHub("owner", "mono")
	gh.Tag("v1.0.0-rc1", monoTag("2.0.0-rc1", "1.0.0"))
	gh.Tag("v1.0.0", monoTag("2.0.0", "1.0.0"))
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	if _, err := f.app.Add(ctx, f.writeFile("mono.json", pkgSameTag)); err != nil {
		t.Fatal(err, f.log)
	}
	res, err := f.app.Pull(ctx, []string{"mono/com.example.pkg"}, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	var built []string
	for _, b := range res.Built {
		built = append(built, fmt.Sprintf("%s %s %s", b.Package, b.Version, b.Origin))
	}
	if want := []string{"com.example.pkg 1.0.0 tag v1.0.0", "com.example.base 2.0.0 tag v1.0.0"}; !reflect.DeepEqual(built, want) {
		t.Errorf("built = %v, want %v", built, want)
	}
	m := readManifest(t, f.app.Layout.PackageDir("mono", "com.example.pkg", "1.0.0"))
	if got, want := m["vpmDependencies"], map[string]any{"com.example.base": "=2.0.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("vpmDependencies = %v, want %v", got, want)
	}
}

// "<id>@<version>" names the version in every package that has it, and
// "<id>/<name>@<version>" in that package only.
func TestPullNamesVersionsByPackage(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	recipe := gitRecipe("mono", "",
		gitPackage("com.example.base", "Packages/Base", ""),
		gitPackage("com.example.pkg", "Packages/Pkg", ""))
	if _, err := f.app.Add(ctx, f.writeFile("mono.json", recipe)); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.Pull(ctx, []string{"mono/com.example.base@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after base@1.0.0: available = %v, want %v (that version only)", got, want)
	}
	// A target without a version beside it still chooses the newest.
	if _, err := f.app.Pull(ctx, []string{"mono@1.0.0", "mono"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3", "1.1.0@2021.3", "1.0.0@2021.3", "1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after mono@1.0.0: available = %v, want %v", got, want)
	}
	if _, err := f.app.Pull(ctx, []string{"mono/com.example.none"}, PullOptions{}); err == nil {
		t.Error("pulled a package the recipe does not have")
	}

	// remove takes the same names.
	res, err := f.app.RemoveVersions("mono/com.example.pkg", "")
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(res.Removed) != 2 || res.RecipeRemoved {
		t.Errorf("remove pkg = %+v", res)
	}
	if _, err := f.app.RemoveVersions("mono", "1.0.0"); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after removing: available = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Join(f.app.Layout.Recipes(), "mono.json")); err != nil {
		t.Errorf("the recipe went with its package: %v", err)
	}
}

// Two recipes may provide one package: each has its own folders, and ALCOM
// chooses among the versions both register.
func TestRecipesThatShareAPackage(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0", "1.1.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	both := gitRecipe("both", "",
		gitPackage("com.example.base", "Packages/Base", ""),
		gitPackage("com.example.pkg", "Packages/Pkg", pkgNeedsBase))
	// The other recipe makes base with another package.json.
	alone := gitRecipe("alone", "", gitPackage("com.example.base", "Packages/Base", `, "overrides": [{"set": {"displayName": "Base"}}]`))
	if err := f.addAndPull(PullOptions{}, f.writeFile("both.json", both), f.writeFile("alone.json", alone)); err != nil {
		t.Fatal(err, f.log)
	}
	mine := f.app.Layout.PackageDir("both", "com.example.base", "1.1.0")
	theirs := f.app.Layout.PackageDir("alone", "com.example.base", "1.1.0")
	registered := strings.Join(f.registered(), "\n")
	if !strings.Contains(registered, mine) || !strings.Contains(registered, theirs) {
		t.Errorf("registered = %s", registered)
	}
	problems, err := f.app.Doctor(false)
	if err != nil {
		t.Fatal(err)
	}
	if msgs := problemMessages(problems); len(msgs) != 1 || !strings.Contains(msgs[0], "com.example.base 1.1.0: recipes alone, both make it with different package.json") {
		t.Errorf("doctor = %v", msgs)
	}

	// The dependency comes from the dependent's own recipe first.
	if _, err := f.app.RemoveVersions("both/com.example.base", ""); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.RemoveRecipe("alone", true); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.Add(ctx, f.writeFile("alone.json", alone)); err != nil {
		t.Fatal(err, f.log)
	}
	res, err := f.app.Pull(ctx, []string{"both/com.example.pkg"}, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	var built []string
	for _, b := range res.Built {
		built = append(built, b.Recipe+" "+b.Package+" "+b.Version)
	}
	if want := []string{"both com.example.base 1.1.0"}; !reflect.DeepEqual(built, want) {
		t.Errorf("built = %v, want %v", built, want)
	}
	// Removing one recipe leaves the other's folders.
	if _, err := f.app.RemoveRecipe("both", true); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.Pull(ctx, []string{"alone"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.RemoveVersions("alone/com.example.base", "1.1.0"); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := os.Stat(filepath.Join(f.app.Layout.Packages(), "alone")); !os.IsNotExist(err) {
		t.Errorf("the recipe's folder is left empty: %v", err)
	}
	if problems, err := f.app.Doctor(false); err != nil || len(problems) != 0 {
		t.Errorf("doctor = %v, %v", problemMessages(problems), err)
	}
}

// A source that cannot be read keeps its last list, and the recipe's other
// sources go on.
func TestUpdateGoesOnPastASourceThatFails(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0")
	f.reg.add("1.0.0", "2021.3")
	f.app.Client.HTTP.Transport = routeByHost{"github.com": gh, "raw.githubusercontent.com": gh, "codeload.github.com": gh, "registry.test": f.reg}
	ctx := context.Background()
	recipe := `{"vpmifyFormat": 1, "displayName": "", "id": "two", "sources": [
  {"type": "github", "url": "` + monoURL + `", "packages": [{"name": "com.example.base", "path": "Packages/Base"}]},
  {"type": "npm", "registry": "` + registryURL + `", "packages": [{"name": "` + pkgName + `"}]}]}`
	if _, err := f.app.Add(ctx, f.writeFile("two.json", recipe)); err != nil {
		t.Fatal(err, f.log)
	}
	gh.Tag("v1.1.0", monoTag("1.1.0", "1.1.0"))
	f.reg.add("1.1.0", "2021.3")
	f.reg.down = true
	up, err := f.app.Update(ctx, nil)
	if err == nil || !strings.Contains(err.Error(), "npm "+registryURL) {
		t.Errorf("update: err = %v", err)
	}
	if len(up.Recipes) != 1 || len(up.Recipes[0].Packages) != 1 || !reflect.DeepEqual(up.Recipes[0].Packages[0].New, []string{"1.1.0"}) {
		t.Errorf("update = %+v", up)
	}
	f.reg.down = false
	if _, err := f.app.Pull(ctx, nil, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	// The npm package comes from the list the failed update kept.
	if got, want := f.available(), []string{"1.1.0@2021.3", "1.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("available = %v, want %v", got, want)
	}
}

// A package taken out of a recipe leaves its versions available, and
// doctor names them.
func TestDoctorNamesAPackageTheRecipeNoLongerHas(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("1.0.0")
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	two := gitRecipe("mono", "",
		gitPackage("com.example.base", "Packages/Base", ""),
		gitPackage("com.example.pkg", "Packages/Pkg", ""))
	if err := f.addAndPull(PullOptions{}, f.writeFile("mono.json", two)); err != nil {
		t.Fatal(err, f.log)
	}
	one := gitRecipe("mono", "", gitPackage("com.example.base", "Packages/Base", ""))
	if _, err := f.app.Add(ctx, f.writeFile("mono.json", one)); err != nil {
		t.Fatal(err, f.log)
	}
	problems, err := f.app.Doctor(false)
	if err != nil {
		t.Fatal(err)
	}
	if msgs := problemMessages(problems); len(msgs) != 1 || !strings.Contains(msgs[0], "`vpmify package remove --all mono/com.example.pkg`") {
		t.Errorf("doctor = %v", msgs)
	}
	if _, err := f.app.RemoveVersions("mono/com.example.pkg", ""); err != nil {
		t.Fatal(err, f.log)
	}
	if problems, err := f.app.Doctor(false); err != nil || len(problems) != 0 {
		t.Errorf("after remove: doctor = %v, %v", problemMessages(problems), err)
	}
}

// routeByHost sends each request to the transport for its host.
type routeByHost map[string]http.RoundTripper

func (r routeByHost) RoundTrip(req *http.Request) (*http.Response, error) {
	if t, ok := r[req.URL.Host]; ok {
		return t.RoundTrip(req)
	}
	return nil, fmt.Errorf("no route to %s", req.URL.Host)
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

// staleNPMRecipe is a recipe whose npm source has no version list, because
// the registry was down when it was added.
func (f *fixture) staleNPMRecipe() {
	f.t.Helper()
	gh := monorepo("1.0.0")
	f.app.Client.HTTP.Transport = routeByHost{"github.com": gh, "raw.githubusercontent.com": gh, "codeload.github.com": gh, "registry.test": f.reg}
	f.reg.down = true
	recipe := `{"vpmifyFormat": 1, "displayName": "", "id": "two", "sources": [
  {"type": "github", "url": "` + monoURL + `", "packages": [
    {"name": "com.example.base", "path": "Packages/Base"},
    {"name": "com.example.pkg", "path": "Packages/Pkg", "overrides": [{"set": {"vpmDependencies": {"com.example.base": "={sameTag}"}}}]}]},
  {"type": "npm", "registry": "` + registryURL + `", "packages": [{"name": "` + pkgName + `"}]}]}`
	if _, err := f.app.Add(context.Background(), f.writeFile("two.json", recipe)); err == nil {
		f.t.Fatal("add read the npm source while the registry was down")
	}
}

// A source without a list fails only what the user chose from it.
func TestAStaleSourceFailsOnlyWhatIsChosenFromIt(t *testing.T) {
	f := newFixture(t)
	f.staleNPMRecipe()
	ctx := context.Background()
	res, err := f.app.Pull(ctx, []string{"two/com.example.pkg"}, PullOptions{})
	if err != nil {
		t.Errorf("pull of a package whose source has a list: %v (%+v)", err, res.Errors)
	}
	if len(res.Built) != 2 {
		t.Errorf("built = %+v, want pkg and the base it needs", res.Built)
	}
	if _, err := f.app.Pull(ctx, []string{"two"}, PullOptions{}); err == nil || !strings.Contains(err.Error(), "npm "+registryURL) {
		t.Errorf("pull of the whole recipe: err = %v", err)
	}
	if _, err := f.app.Pull(ctx, []string{"two/" + pkgName + "@1.0.0"}, PullOptions{}); err == nil || !strings.Contains(err.Error(), "npm "+registryURL) {
		t.Errorf("pull of a version of the stale package: err = %v", err)
	}
	// versions shows what it can, with the error.
	list, err := f.app.Versions("two", VersionsOptions{})
	if err == nil || list == nil || len(list.Packages) != 2 {
		t.Errorf("versions = %+v, %v; want the two github packages and an error", list, err)
	}
}

// remove of a package with no version in the store is an error, even when
// pull left an empty record for it.
func TestRemoveOfAPackageWithoutVersions(t *testing.T) {
	f := newFixture(t)
	f.app.Client.HTTP.Transport = monorepo("1.0.0")
	ctx := context.Background()
	recipe := gitRecipe("mono", "",
		gitPackage("com.example.base", "Packages/Base", ""),
		gitPackage("com.example.pkg", "Packages/Pkg", ""))
	if _, err := f.app.Add(ctx, f.writeFile("mono.json", recipe)); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.RemoveVersions("mono/com.example.base", ""); err == nil || !strings.Contains(err.Error(), "no version of com.example.base is in the store") {
		t.Errorf("before pull: err = %v", err)
	}
	if _, err := f.app.Pull(ctx, []string{"mono/com.example.pkg"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.RemoveVersions("mono/com.example.base", ""); err == nil || !strings.Contains(err.Error(), "no version of com.example.base is in the store") {
		t.Errorf("after pull: err = %v", err)
	}
}

// "<id>@<version>" passes over a package that has the version but cannot
// convert it, and fails only when no package can.
func TestPullOfAVersionSkipsPackagesThatCannotConvertIt(t *testing.T) {
	f := newFixture(t)
	gh := testutil.NewFakeGitHub("owner", "mono")
	// base and pkg both have 1.2.0 in v1.2.0; pkg's {sameTag} base comes
	// from v1.0.0, which base's paths do not read.
	gh.Tag("v1.0.0", monoTag("1.0.0", "1.2.0"))
	gh.Tag("v1.2.0", monoTag("1.2.0", "1.3.0"))
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	recipe := gitRecipe("mono", "",
		gitPackage("com.example.base", "", `, "paths": [{"tags": ">=1.2.0", "path": "Packages/Base"}]`),
		gitPackage("com.example.pkg", "Packages/Pkg", `, "overrides": [{"set": {"vpmDependencies": {"com.example.base": "={sameTag}"}}}]`))
	if _, err := f.app.Add(ctx, f.writeFile("mono.json", recipe)); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.Pull(ctx, []string{"mono/com.example.base@1.2.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	// 1.2.0 of pkg cannot be converted; base's is.
	res, err := f.app.Pull(ctx, []string{"mono@1.2.0"}, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	for _, b := range res.Built {
		if b.Package == "com.example.pkg" && b.Version == "1.2.0" {
			t.Errorf("built pkg 1.2.0")
		}
	}
	if _, err := f.app.Pull(ctx, []string{"mono/com.example.pkg@1.2.0"}, PullOptions{}); err == nil || !strings.Contains(err.Error(), "same tag has no version") {
		t.Errorf("pkg@1.2.0 named: err = %v", err)
	}
}

// A package whose package.json does not tell its tags apart takes the
// version of the tag, which Vpmify writes into package.json, as for
// Warudo-Mod-Tool.
func TestVersionFromTagIsWrittenIntoPackageJSON(t *testing.T) {
	f := newFixture(t)
	gh := testutil.NewFakeGitHub("owner", "mono")
	gh.Tag("0.15.2.1", monoTag("1.0.0", "0.15.2"))
	gh.Tag("0.15.2.2", monoTag("1.0.0", "0.15.2"))
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	recipe := fmt.Sprintf(`{
  "vpmifyFormat": 1, "displayName": "", "id": "mono",
  "sources": [{"type": "github", "url": %q,
    "tagVersion": {"match": "(\\d+)\\.(\\d+)\\.(\\d+)\\.(\\d+)", "version": "{1}.{2}.{3}{4:03}"},
    "packages": [{"name": "com.example.pkg", "path": "Packages/Pkg", "versionFromTag": true}]}]
}`, monoURL)
	if _, err := f.app.Add(ctx, f.writeFile("mono.json", recipe)); err != nil {
		t.Fatal(err, f.log)
	}
	res, err := f.app.Pull(ctx, []string{"mono/com.example.pkg@0.15.2001"}, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	var built []string
	for _, b := range res.Built {
		built = append(built, b.Version)
	}
	if want := []string{"0.15.2001"}; !reflect.DeepEqual(built, want) {
		t.Errorf("built = %v, want %v", built, want)
	}
	for _, v := range built {
		m := readManifest(t, f.app.Layout.PackageDir("mono", "com.example.pkg", v))
		if m["version"] != v {
			t.Errorf("%s: package.json version = %v", v, m["version"])
		}
	}
}

// Tags that a recipe's rule reads as one version are a warning for whoever
// keeps the recipe, in the log and the result, and the version list is
// still read, from the older tag.
func TestTagsOfOneVersionWarn(t *testing.T) {
	f := newFixture(t)
	gh := testutil.NewFakeGitHub("owner", "mono")
	gh.Tag("0.15.2.1", monoTag("1.0.0", "0.15.2"))
	gh.Tag("0.15.2.2", monoTag("1.0.0", "0.15.2"))
	f.app.Client.HTTP.Transport = gh
	recipe := fmt.Sprintf(`{
  "vpmifyFormat": 1, "displayName": "", "id": "mono",
  "sources": [{"type": "github", "url": %q,
    "tagVersion": {"match": "(\\d+)\\.(\\d+)\\.(\\d+)\\.(\\d+)", "version": "{1}.{2}.{3}"},
    "packages": [{"name": "com.example.pkg", "path": "Packages/Pkg"}]}]
}`, monoURL)
	res, err := f.app.Add(context.Background(), f.writeFile("mono.json", recipe))
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(res.Recipes) != 1 || len(res.Recipes[0].Warnings) != 1 || !strings.Contains(res.Recipes[0].Warnings[0], "com.example.pkg is taken from 0.15.2.1") {
		t.Errorf("result = %+v", res.Recipes)
	}
	if !strings.Contains(f.log.String(), "mono: warning: tags 0.15.2.1 and 0.15.2.2") {
		t.Errorf("log = %s", f.log)
	}
	list, err := f.app.Versions("mono", VersionsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v := list.Packages[0].Versions; len(v) != 1 || v[0].Version != "0.15.2" {
		t.Errorf("versions = %+v", v)
	}
}
