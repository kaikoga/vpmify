package source

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/recipe"
	"github.com/kaikoga/vpmify/internal/testutil"
)

func manifest(name, version string) string {
	return fmt.Sprintf(`{"name": %q, "version": %q, "unity": "2021.3", "dependencies": {"com.example.base": %q}}`, name, version, version)
}

// newSource returns the upstream of the first source of recipeJSON.
func newSource(t *testing.T, gh *testutil.FakeGitHub, recipeJSON string) *GitHub {
	t.Helper()
	r, err := recipe.Parse([]byte(recipeJSON))
	if err != nil {
		t.Fatal(err)
	}
	client := fetch.NewClient("vpmify-test")
	client.HTTP.Transport = gh
	g, err := NewGitHub(r.Sources[0], client, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// warudoRepo has tags like Warudo-Mod-Tool's: four numbers, and a
// package.json version that does not tell them apart or runs behind.
func warudoRepo() *testutil.FakeGitHub {
	gh := testutil.NewFakeGitHub("owner", "repo")
	gh.Tag("0.14.3", map[string]string{"package.json": manifest("com.example.pkg", "0.14.3")})
	gh.Tag("0.14.4.2", map[string]string{"package.json": manifest("com.example.pkg", "0.14.3")})
	gh.Tag("0.15.2.1", map[string]string{"package.json": manifest("com.example.pkg", "0.15.2")})
	gh.Tag("0.15.2.2", map[string]string{"package.json": manifest("com.example.pkg", "0.15.2")})
	gh.Tag("upm/latest", map[string]string{"package.json": manifest("com.example.pkg", "0.15.2")})
	// No version: not read, as without versionFromTag, so that the index
	// holds no version the conversion cannot check.
	gh.Tag("0.15.3.1", map[string]string{"package.json": `{"name": "com.example.pkg"}`})
	return gh
}

func warudoRecipe(version, pkg string) string {
	return `{"id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
  "tagVersion": {"match": "(\\d+)\\.(\\d+)\\.(\\d+)(?:\\.(\\d+))?", "version": "` + version + `"},
  "tags": ">=0.14.4000",
  "packages": [{"name": "com.example.pkg"` + pkg + `}]}]}`
}

func TestGitHubTagVersion(t *testing.T) {
	gh := warudoRepo()
	src := newSource(t, gh, warudoRecipe("{1}.{2}.{3}{4:03}", `, "versionFromTag": true`))
	all, _, err := src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var summary []string
	for _, r := range all["com.example.pkg"] {
		summary = append(summary, r.Version.String()+" "+r.UpstreamVersion+" "+r.Origin)
	}
	// Each tag is a version of its own; 0.14.3 is out of the tags range.
	want := []string{
		"0.14.4002 0.14.3 tag 0.14.4.2",
		"0.15.2001 0.15.2 tag 0.15.2.1",
		"0.15.2002 0.15.2 tag 0.15.2.2",
	}
	if !reflect.DeepEqual(summary, want) {
		t.Errorf("releases = %q, want %q", summary, want)
	}

	// Without versionFromTag the tags are read by the rule, and the
	// versions are package.json's, the oldest tag winning as ever.
	src = newSource(t, gh, warudoRecipe("{1}.{2}.{3}{4:03}", ""))
	all, _, err = src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	summary = nil
	for _, r := range all["com.example.pkg"] {
		summary = append(summary, r.Version.String()+" ["+r.UpstreamVersion+"] "+r.Origin)
	}
	if want := []string{"0.14.3 [] tag 0.14.4.2", "0.15.2 [] tag 0.15.2.1"}; !reflect.DeepEqual(summary, want) {
		t.Errorf("releases = %q, want %q", summary, want)
	}

	// A rule that reads two tags as one version warns, and the older tag is
	// used (kaikoga 2026-11-19): the recipe needs updating, but a tag the
	// repository adds must not stop it. Names order the tags, numbers by
	// value: 0.15.2.9 is older than 0.15.2.10.
	gh.Tag("0.15.2.9", map[string]string{"package.json": manifest("com.example.pkg", "0.15.2")})
	gh.Tag("0.15.2.10", map[string]string{"package.json": manifest("com.example.pkg", "0.15.2")})
	src = newSource(t, gh, `{"id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
  "tagVersion": {"match": "(\\d+)\\.(\\d+)\\.(\\d+)\\.(\\d+)", "version": "{1}.{2}.{3}"},
  "tags": ">=0.15.0",
  "packages": [{"name": "com.example.pkg", "versionFromTag": true}]}]}`)
	all, warnings, err := src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	summary = nil
	for _, r := range all["com.example.pkg"] {
		summary = append(summary, r.Version.String()+" "+r.Origin)
	}
	if want := []string{"0.15.2 tag 0.15.2.1"}; !reflect.DeepEqual(summary, want) {
		t.Errorf("releases = %q, want %q", summary, want)
	}
	if len(warnings) != 3 || !strings.Contains(warnings[0], "tags 0.15.2.1 and 0.15.2.2 both read as version 0.15.2, and 0.15.2.1 is used") ||
		!strings.Contains(warnings[2], "tags 0.15.2.9 and 0.15.2.10") {
		t.Errorf("warnings = %q", warnings)
	}
}

// A package that keeps one version through tags whose contents differ, and
// tells its releases apart from some tag on (UniVRM's com.vrmc.unigltf is
// 2.0.0 from v0.63.0 to v0.65.2): versionFromTag with a range of tags gives
// the tags in the range their own versions, and leaves the others to
// package.json.
func TestGitHubVersionFromTagInARangeOfTags(t *testing.T) {
	gh := testutil.NewFakeGitHub("owner", "repo")
	for _, v := range []struct{ tag, base string }{
		{"0.63.0", "2.0.0"}, {"0.64.0", "2.0.0"}, {"0.65.2", "2.0.0"}, {"0.65.3", "2.1.3"},
	} {
		gh.Tag("v"+v.tag, map[string]string{
			"Base/package.json": manifest("com.example.base", v.base),
			"Pkg/package.json":  manifest("com.example.pkg", v.tag),
		})
	}
	src := newSource(t, gh, `{"id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
  "packages": [{"name": "com.example.base", "path": "Base", "versionFromTag": {"tags": "<0.65.2"}},
    {"name": "com.example.pkg", "path": "Pkg"}]}]}`)
	all, _, err := src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var summary []string
	for _, r := range all["com.example.base"] {
		summary = append(summary, r.Version.String()+" ["+r.UpstreamVersion+"] "+r.Origin)
	}
	want := []string{
		"0.63.0 [2.0.0] tag v0.63.0",
		"0.64.0 [2.0.0] tag v0.64.0",
		"2.0.0 [] tag v0.65.2",
		"2.1.3 [] tag v0.65.3",
	}
	if !reflect.DeepEqual(summary, want) {
		t.Errorf("releases = %q, want %q", summary, want)
	}
	// The other package of each tag names the version the tag's base got.
	summary = nil
	for _, r := range all["com.example.pkg"] {
		summary = append(summary, r.Version.String()+" "+r.SameTag["com.example.base"])
	}
	if want := []string{"0.63.0 0.63.0", "0.64.0 0.64.0", "0.65.2 2.0.0", "0.65.3 2.1.3"}; !reflect.DeepEqual(summary, want) {
		t.Errorf("sameTag = %q, want %q", summary, want)
	}
}

func TestGitHubReleases(t *testing.T) {
	gh := testutil.NewFakeGitHub("owner", "repo")
	gh.Tag("v0.9.0", map[string]string{"Assets/Other/package.json": manifest("com.example.other", "0.9.0")})
	gh.Tag("v1.0.0", map[string]string{"Assets/Pkg/package.json": manifest("com.example.pkg", "1.0.0")})
	// The package moved, and the version number ran ahead of the tag.
	v110 := gh.Tag("v1.1.0", map[string]string{"Packages/Pkg/package.json": manifest("com.example.pkg", "2.0.0")})
	// The same version again in a newer tag: the oldest tag wins.
	gh.Tag("v1.2.0", map[string]string{"Packages/Pkg/package.json": manifest("com.example.pkg", "2.0.0")})
	// Renamed: another package now.
	gh.Tag("v1.3.0", map[string]string{"Packages/Pkg/package.json": manifest("com.example.renamed", "1.3.0")})
	// Not SemVer, and not matching the pattern.
	gh.Tag("v0.28", map[string]string{"Assets/Pkg/package.json": manifest("com.example.pkg", "0.28.0")})
	gh.Tag("Samples", map[string]string{"Assets/Pkg/package.json": manifest("com.example.pkg", "0.0.1")})

	src := newSource(t, gh, `{
  "id": "pkg",
  "sources": [{"type": "github", "url": "https://github.com/owner/repo.git", "tagPattern": "v*", "tags": ">=0.9.0",
    "packages": [{"name": "com.example.pkg",
      "paths": [{"tags": ">=1.1.0", "path": "Packages/Pkg"}, {"tags": "<1.1.0", "path": "Assets/Pkg"}]}]}]
}`)
	all, _, err := src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := all["com.example.pkg"]
	var summary []string
	for _, r := range got {
		summary = append(summary, r.Version.String()+" "+r.Dir+" "+r.Origin)
	}
	want := []string{
		"1.0.0 Assets/Pkg tag v1.0.0",
		"2.0.0 Packages/Pkg tag v1.1.0",
	}
	if !reflect.DeepEqual(summary, want) {
		t.Errorf("releases = %q, want %q", summary, want)
	}
	// Each tag reads one folder: the five tags v0.9.0 to v1.3.0.
	if n := gh.Requests["raw"]; n != 5 {
		t.Errorf("raw requests = %d, want 5", n)
	}
	r := got[1]
	if r.ArchiveURL != "https://codeload.github.com/owner/repo/tar.gz/"+v110 || !r.Pinned {
		t.Errorf("archive = %s pinned=%v", r.ArchiveURL, r.Pinned)
	}
	if r.Dependencies["com.example.base"] != "2.0.0" || r.Unity != "2021.3" || r.SameTag != nil {
		t.Errorf("manifest fields = %+v", r)
	}

	// package.json of each commit is cached, missing ones too.
	before := gh.Requests["raw"]
	if _, _, err := src.Releases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := gh.Requests["raw"]; after != before {
		t.Errorf("raw requests went from %d to %d on the second listing", before, after)
	}
}

// Overlays are chosen by the tag's version, which the version list does not
// keep, not by the package's.
func TestGitHubOverlaysFollowTheTag(t *testing.T) {
	gh := testutil.NewFakeGitHub("owner", "repo")
	gh.Tag("v1.0.0", map[string]string{"package.json": manifest("com.example.pkg", "1.0.0")})
	gh.Tag("v1.1.0", map[string]string{"package.json": manifest("com.example.pkg", "3.0.0")})
	const a, b = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	recipeJSON := `{"id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo", "tagPattern": "v*",
  "packages": [{"name": "com.example.pkg", "overlays": [
    {"tags": "<1.1.0", "path": "Sub", "url": "https://github.com/other/sub.git", "commit": "` + a + `"},
    {"tags": ">=1.1.0", "path": "Lib/Sub", "url": "https://github.com/other/sub", "commit": "` + b + `"}]}]}]}`
	all, _, err := newSource(t, gh, recipeJSON).Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range all["com.example.pkg"] {
		for _, o := range r.Overlays {
			got = append(got, r.Version.String()+" "+o.Path+" "+o.ArchiveURL+" "+o.CacheKey)
		}
	}
	want := []string{
		"1.0.0 Sub https://codeload.github.com/other/sub/tar.gz/" + a + " git/github.com/other/sub/" + a + ".tar.gz",
		"3.0.0 Lib/Sub https://codeload.github.com/other/sub/tar.gz/" + b + " git/github.com/other/sub/" + b + ".tar.gz",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("overlays = %q, want %q", got, want)
	}

	// An overlay from elsewhere than GitHub is refused before anything is read.
	r, err := recipe.Parse([]byte(strings.Replace(recipeJSON, "https://github.com/other/sub.git", "https://gitlab.com/other/sub", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewGitHub(r.Sources[0], fetch.NewClient("vpmify-test"), t.TempDir()); err == nil || !strings.Contains(err.Error(), "overlays[0]") {
		t.Errorf("err = %v", err)
	}
}

func TestGitHubSameTag(t *testing.T) {
	gh := testutil.NewFakeGitHub("owner", "repo")
	// The base's numbers differ from the tag's.
	gh.Tag("v1.0.0", map[string]string{
		"Base/package.json": manifest("com.example.base", "2.16.0"),
		"Pkg/package.json":  manifest("com.example.pkg", "1.0.0"),
	})
	// The package stays at 1.0.0 in a newer tag: it keeps the oldest tag's
	// base.
	gh.Tag("v1.0.1", map[string]string{
		"Base/package.json": manifest("com.example.base", "2.17.0"),
		"Pkg/package.json":  manifest("com.example.pkg", "1.0.0"),
	})
	// No base in this tag.
	gh.Tag("v1.1.0", map[string]string{"Pkg/package.json": manifest("com.example.pkg", "1.1.0")})
	src := newSource(t, gh, `{"id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
  "packages": [{"name": "com.example.base", "path": "Base"}, {"name": "com.example.pkg", "path": "Pkg"}]}]}`)
	all, _, err := src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var summary []string
	for _, name := range []string{"com.example.base", "com.example.pkg"} {
		for _, r := range all[name] {
			summary = append(summary, fmt.Sprintf("%s %s %s %v", name, r.Version, r.Origin, r.SameTag))
		}
	}
	want := []string{
		"com.example.base 2.16.0 tag v1.0.0 map[com.example.pkg:1.0.0]",
		"com.example.base 2.17.0 tag v1.0.1 map[com.example.pkg:1.0.0]",
		"com.example.pkg 1.0.0 tag v1.0.0 map[com.example.base:2.16.0]",
		"com.example.pkg 1.1.0 tag v1.1.0 map[]",
	}
	if !reflect.DeepEqual(summary, want) {
		t.Errorf("releases =\n%q\nwant\n%q", summary, want)
	}
	// One listing of the tags for both packages.
	if n := gh.Requests["refs"]; n != 1 {
		t.Errorf("refs requests = %d, want 1", n)
	}
}

// A stable version first seen in a release candidate's tag is taken from
// the release's tag; a prerelease stays with its oldest tag.
func TestGitHubStableVersionComesFromAStableTag(t *testing.T) {
	gh := testutil.NewFakeGitHub("owner", "repo")
	gh.Tag("v1.0.0-rc1", map[string]string{
		"Base/package.json": manifest("com.example.base", "2.0.0-rc1"),
		"Pkg/package.json":  manifest("com.example.pkg", "1.0.0"),
	})
	release := gh.Tag("v1.0.0", map[string]string{
		"Base/package.json": manifest("com.example.base", "2.0.0"),
		"Pkg/package.json":  manifest("com.example.pkg", "1.0.0"),
	})
	gh.Tag("v1.0.1", map[string]string{
		"Base/package.json": manifest("com.example.base", "2.0.1"),
		"Pkg/package.json":  manifest("com.example.pkg", "1.0.0"),
	})
	src := newSource(t, gh, `{"id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
  "packages": [{"name": "com.example.base", "path": "Base"}, {"name": "com.example.pkg", "path": "Pkg"}]}]}`)
	all, _, err := src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pkg := all["com.example.pkg"]
	if len(pkg) != 1 || pkg[0].Origin != "tag v1.0.0" || pkg[0].SameTag["com.example.base"] != "2.0.0" ||
		pkg[0].ArchiveURL != "https://codeload.github.com/owner/repo/tar.gz/"+release {
		t.Errorf("pkg = %+v", pkg)
	}
	if base := all["com.example.base"]; len(base) != 3 || base[0].Version.String() != "2.0.0-rc1" || base[0].Origin != "tag v1.0.0-rc1" {
		t.Errorf("base = %+v", base)
	}
}

func TestGitHubTagsRangeLimitsReads(t *testing.T) {
	gh := testutil.NewFakeGitHub("owner", "repo")
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		gh.Tag("v"+v, map[string]string{"package.json": manifest("com.example.pkg", v)})
	}
	src := newSource(t, gh, `{
  "id": "pkg",
  "sources": [{"type": "github", "url": "https://github.com/owner/repo", "tags": ">=1.1.0", "packages": [{"name": "com.example.pkg"}]}]
}`)
	all, _, err := src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := all["com.example.pkg"]
	if len(got) != 2 || got[0].Version.String() != "1.1.0" || got[0].Dir != "" {
		t.Errorf("releases = %+v", got)
	}
	if gh.Requests["raw"] != 2 {
		t.Errorf("raw requests = %d, want 2", gh.Requests["raw"])
	}
}

func TestParseRefAdvertisementPeelsAnnotatedTags(t *testing.T) {
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	tagObj := "1111111111111111111111111111111111111111"
	commit := "2222222222222222222222222222222222222222"
	light := "3333333333333333333333333333333333333333"
	data := pkt("# service=git-upload-pack\n") + "0000" +
		pkt(light+" HEAD\x00caps\n") +
		pkt(tagObj+" refs/tags/v1.0.0\n") +
		pkt(commit+" refs/tags/v1.0.0^{}\n") +
		pkt(light+" refs/tags/v0.9.0\n") + "0000"
	refs, err := parseRefAdvertisement([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if refs["refs/tags/v1.0.0"] != commit || refs["refs/tags/v0.9.0"] != light || refs["HEAD"] != light {
		t.Errorf("refs = %v", refs)
	}
	if _, err := parseRefAdvertisement([]byte("zzzz")); err == nil {
		t.Error("bad length accepted")
	}
}

func TestParseGitHubURL(t *testing.T) {
	for _, ok := range []string{"https://github.com/a/b", "https://github.com/a/b.git", "https://github.com/a/b/"} {
		if o, r, err := parseGitHubURL(ok); err != nil || o != "a" || r != "b" {
			t.Errorf("%s: %s %s %v", ok, o, r, err)
		}
	}
	for _, bad := range []string{"http://github.com/a/b", "https://gitlab.com/a/b", "https://github.com/a", "https://github.com/a/b/c", "git@github.com:a/b.git"} {
		if _, _, err := parseGitHubURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
