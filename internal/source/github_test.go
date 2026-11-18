package source

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/recipe"
	"github.com/kaikoga/vpmify/internal/testutil"
)

func manifest(name, version string) string {
	return fmt.Sprintf(`{"name": %q, "version": %q, "unity": "2021.3", "dependencies": {"com.example.base": %q}}`, name, version, version)
}

func newSource(t *testing.T, gh *testutil.FakeGitHub, recipeJSON string) *GitHub {
	t.Helper()
	r, err := recipe.Parse([]byte(recipeJSON))
	if err != nil {
		t.Fatal(err)
	}
	client := fetch.NewClient("vpmify-test")
	client.HTTP.Transport = gh
	g, err := NewGitHub(r, client, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return g
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
  "source": {"type": "github", "url": "https://github.com/owner/repo.git", "name": "com.example.pkg", "tagPattern": "v*", "tags": ">=0.9.0"},
  "paths": [{"tags": ">=1.1.0", "path": "Packages/Pkg"}, {"tags": "<1.1.0", "path": "Assets/Pkg"}]
}`)
	got, err := src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
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
	if r.Dependencies["com.example.base"] != "2.0.0" || r.Unity != "2021.3" {
		t.Errorf("manifest fields = %+v", r)
	}

	// package.json of each commit is cached, missing ones too.
	before := gh.Requests["raw"]
	if _, err := src.Releases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := gh.Requests["raw"]; after != before {
		t.Errorf("raw requests went from %d to %d on the second listing", before, after)
	}
}

func TestGitHubTagsRangeLimitsReads(t *testing.T) {
	gh := testutil.NewFakeGitHub("owner", "repo")
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		gh.Tag("v"+v, map[string]string{"package.json": manifest("com.example.pkg", v)})
	}
	src := newSource(t, gh, `{
  "id": "pkg",
  "source": {"type": "github", "url": "https://github.com/owner/repo", "name": "com.example.pkg", "tags": ">=1.1.0"}
}`)
	got, err := src.Releases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
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
