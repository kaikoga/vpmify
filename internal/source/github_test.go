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
	return `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
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
	// used: the recipe needs updating, but a tag the
	// repository adds must not stop it. Names order the tags, numbers by
	// value: 0.15.2.9 is older than 0.15.2.10.
	gh.Tag("0.15.2.9", map[string]string{"package.json": manifest("com.example.pkg", "0.15.2")})
	gh.Tag("0.15.2.10", map[string]string{"package.json": manifest("com.example.pkg", "0.15.2")})
	src = newSource(t, gh, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
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
	// One warning per version names every tag of it, and the one used.
	if len(warnings) != 1 || !strings.Contains(warnings[0], "tags 0.15.2.1, 0.15.2.2, 0.15.2.9 and 0.15.2.10 read as version 0.15.2, and com.example.pkg is taken from 0.15.2.1") {
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
	src := newSource(t, gh, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
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
	// Not SemVer, with and without the "v".
	gh.Tag("v0.28", map[string]string{"Assets/Pkg/package.json": manifest("com.example.pkg", "0.28.0")})
	gh.Tag("Samples", map[string]string{"Assets/Pkg/package.json": manifest("com.example.pkg", "0.0.1")})

	src := newSource(t, gh, `{
  "vpmifyFormat": 1, "id": "pkg",
  "sources": [{"type": "github", "url": "https://github.com/owner/repo.git", "tags": ">=0.9.0",
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
	recipeJSON := `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
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
}

// Without a tagVersion rule, v1.0.0 and 1.0.0 read as one version: the
// older by name is used, with a warning.
func TestGitHubWarnsOfTagsThatReadAsOneVersionWithoutARule(t *testing.T) {
	gh := testutil.NewFakeGitHub("owner", "repo")
	gh.Tag("v1.0.0", map[string]string{"package.json": manifest("com.example.pkg", "1.0.0")})
	gh.Tag("1.0.0", map[string]string{"package.json": manifest("com.example.pkg", "1.0.0")})
	gh.Tag("v1.1.0", map[string]string{"package.json": manifest("com.example.pkg", "1.1.0")})
	src := newSource(t, gh, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
  "packages": [{"name": "com.example.pkg"}]}]}`)
	all, warnings, err := src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var summary []string
	for _, r := range all["com.example.pkg"] {
		summary = append(summary, r.Version.String()+" "+r.Origin)
	}
	if want := []string{"1.0.0 tag 1.0.0", "1.1.0 tag v1.1.0"}; !reflect.DeepEqual(summary, want) {
		t.Errorf("releases = %q, want %q", summary, want)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "tags 1.0.0 and v1.0.0 read as version 1.0.0, and com.example.pkg is taken from 1.0.0") {
		t.Errorf("warnings = %q", warnings)
	}
}

// A tag that makes no package version is not used, and not in the warning:
// v2.0.0 is used though 2.0.0 is older, and 3.0.0 beside v3.0.0 warns of
// nothing.
func TestGitHubWarnsOnlyOfTagsThatMakeAVersion(t *testing.T) {
	gh := testutil.NewFakeGitHub("owner", "repo")
	gh.Tag("2.0.0", map[string]string{"package.json": manifest("com.example.other", "2.0.0")})
	gh.Tag("v2.0.0", map[string]string{"package.json": manifest("com.example.pkg", "2.0.0")})
	gh.Tag("v2.0.0-old", map[string]string{"package.json": manifest("com.example.pkg", "2.0.0")})
	gh.Tag("3.0.0", map[string]string{"README.md": ""})
	gh.Tag("v3.0.0", map[string]string{"README.md": ""})
	src := newSource(t, gh, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
  "tagVersion": {"match": "v?(\\d+\\.\\d+\\.\\d+)(-old)?", "version": "{1}"},
  "packages": [{"name": "com.example.pkg"}]}]}`)
	all, warnings, err := src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := all["com.example.pkg"]; len(got) != 1 || got[0].Origin != "tag v2.0.0" {
		t.Errorf("releases = %+v", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "tags v2.0.0 and v2.0.0-old read as version 2.0.0, and com.example.pkg is taken from v2.0.0") {
		t.Errorf("warnings = %q", warnings)
	}
}

// Versions that differ only in build metadata are one version, taken from
// the oldest tag, as ALCOM cannot tell them apart. From package.json they
// come in tags of different versions and warn of nothing, as any version
// kept in newer tags; from tags read as one version they warn.
func TestGitHubBuildMetadataMakesNoOtherVersion(t *testing.T) {
	gh := testutil.NewFakeGitHub("owner", "repo")
	gh.Tag("v1.0.0", map[string]string{"package.json": manifest("com.example.pkg", "1.0.0+a")})
	gh.Tag("v1.0.1", map[string]string{"package.json": manifest("com.example.pkg", "1.0.0+b")})
	gh.Tag("v1.1.0", map[string]string{"package.json": manifest("com.example.pkg", "1.1.0")})
	all, warnings, err := newSource(t, gh, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
  "packages": [{"name": "com.example.pkg"}]}]}`).Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var summary []string
	for _, r := range all["com.example.pkg"] {
		summary = append(summary, r.Version.String()+" "+r.Origin)
	}
	if want := []string{"1.0.0+a tag v1.0.0", "1.1.0 tag v1.1.0"}; !reflect.DeepEqual(summary, want) {
		t.Errorf("from package.json: releases = %q, want %q", summary, want)
	}
	if len(warnings) != 0 {
		t.Errorf("from package.json: warnings = %q", warnings)
	}

	gh = testutil.NewFakeGitHub("owner", "repo")
	gh.Tag("v1.0.0+a", map[string]string{"package.json": manifest("com.example.pkg", "1.0.0")})
	gh.Tag("v1.0.0+b", map[string]string{"package.json": manifest("com.example.pkg", "1.0.0")})
	all, warnings, err = newSource(t, gh, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
  "packages": [{"name": "com.example.pkg", "versionFromTag": true}]}]}`).Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := all["com.example.pkg"]; len(got) != 1 || got[0].Version.String() != "1.0.0+a" || got[0].Origin != "tag v1.0.0+a" {
		t.Errorf("from tags: releases = %+v", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "tags v1.0.0+a and v1.0.0+b read as version 1.0.0+a, and com.example.pkg is taken from v1.0.0+a") {
		t.Errorf("from tags: warnings = %q", warnings)
	}
}

// Names that differ only in leading zeros, which compare equal by value,
// are ordered by their text, so that the tag taken does not change from run
// to run with the order the ref advertisement comes in.
func TestGitHubOrdersTagsOfEqualNamesByText(t *testing.T) {
	gh := testutil.NewFakeGitHub("owner", "repo")
	for _, name := range []string{"r1.0.0", "r01.0.0", "r001.0.0"} {
		gh.Tag(name, map[string]string{"package.json": manifest("com.example.pkg", "1.0.0")})
	}
	src := newSource(t, gh, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
  "tagVersion": {"match": "r(\\d+)\\.(\\d+)\\.(\\d+)", "version": "{1}.{2}.{3}"},
  "packages": [{"name": "com.example.pkg"}]}]}`)
	for range 20 {
		all, _, err := src.Releases(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got := all["com.example.pkg"]; len(got) != 1 || got[0].Origin != "tag r001.0.0" {
			t.Fatalf("releases = %+v", got)
		}
	}
}

// The warning follows what each package takes. Tags whose package.json
// keeps a version that an older tag has are not used, and warn of nothing;
// two tags of one tag version that hold different package versions are both
// used; and a tag that holds only some of the packages counts only for them.
func TestGitHubWarnsOfTheTagsEachPackageTakes(t *testing.T) {
	recipe := `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
  "packages": [{"name": "com.example.a", "path": "A"}, {"name": "com.example.b", "path": "B"}]}]}`
	cases := []struct {
		name string
		tags map[string]map[string]string
		want []string
	}{
		{"unused", map[string]map[string]string{
			"1.0.0":  {"A/package.json": manifest("com.example.a", "1.0.0")},
			"1.0.1":  {"A/package.json": manifest("com.example.a", "1.0.0")},
			"v1.0.1": {"A/package.json": manifest("com.example.a", "1.0.0")},
		}, nil},
		{"different versions", map[string]map[string]string{
			"1.0.0":  {"A/package.json": manifest("com.example.a", "1.0.0")},
			"v1.0.0": {"A/package.json": manifest("com.example.a", "1.0.1")},
		}, nil},
		{"some packages", map[string]map[string]string{
			"1.0.0":  {"A/package.json": manifest("com.example.a", "1.0.0")},
			"v1.0.0": {"A/package.json": manifest("com.example.a", "1.0.0"), "B/package.json": manifest("com.example.b", "1.0.0")},
		}, []string{"tags 1.0.0 and v1.0.0 read as version 1.0.0, and com.example.a is taken from 1.0.0"}},
		{"both packages", map[string]map[string]string{
			"1.0.0":  {"A/package.json": manifest("com.example.a", "1.0.0"), "B/package.json": manifest("com.example.b", "1.0.0")},
			"v1.0.0": {"A/package.json": manifest("com.example.a", "1.0.0"), "B/package.json": manifest("com.example.b", "1.0.0")},
		}, []string{"tags 1.0.0 and v1.0.0 read as version 1.0.0, and com.example.a and com.example.b are taken from 1.0.0"}},
	}
	for _, c := range cases {
		gh := testutil.NewFakeGitHub("owner", "repo")
		for name, files := range c.tags {
			gh.Tag(name, files)
		}
		_, warnings, err := newSource(t, gh, recipe).Releases(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ok := len(warnings) == len(c.want)
		for i := 0; ok && i < len(c.want); i++ {
			ok = strings.Contains(warnings[i], c.want[i])
		}
		if !ok {
			t.Errorf("%s: warnings = %q, want %q", c.name, warnings, c.want)
		}
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
	src := newSource(t, gh, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
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
	src := newSource(t, gh, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://github.com/owner/repo",
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
  "vpmifyFormat": 1, "id": "pkg",
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
