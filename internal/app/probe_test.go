package app

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/kaikoga/vpmify/internal/fsutil"
	"github.com/kaikoga/vpmify/internal/source"
)

// Probing reads a recipe that is not added, keeps nothing, and says why
// each tag or version it leaves out is left out.
func TestProbeSaysWhatTheSourceLeavesOut(t *testing.T) {
	f := newFixture(t)
	gh := monorepo("0.9.0", "1.0.0", "1.1.0")
	gh.Tag("latest", monoTag("1.1.0", "1.1.0"))
	gh.Tag("v2.0.0", map[string]string{"README.md": "root"})
	f.app.Client.HTTP.Transport = gh
	ctx := context.Background()
	p := f.writeFile("mono.json", gitRecipe("mono", `, "tags": ">=1.0.0"`,
		gitPackage("com.example.base", "Packages/Base", ""),
		gitPackage("com.example.pkg", "Packages/Pkg", `, "versions": "<1.1.0"`)))
	res, err := f.app.Probe(ctx, p)
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(res.Sources) != 1 || res.Sources[0].Error != "" {
		t.Fatalf("sources = %+v", res.Sources)
	}
	s := res.Sources[0]
	var taken []string
	for _, pkg := range s.Packages {
		for _, v := range pkg.Versions {
			taken = append(taken, pkg.Package+" "+v.Version+" "+v.Tag)
		}
	}
	if want := []string{"com.example.base 1.0.0 v1.0.0", "com.example.base 1.1.0 v1.1.0", "com.example.pkg 1.0.0 v1.0.0"}; !reflect.DeepEqual(taken, want) {
		t.Errorf("taken = %v, want %v", taken, want)
	}
	for _, want := range []source.Skip{
		{Tag: "latest", Reason: "the name is not a version"},
		{Tag: "v0.9.0", Version: "0.9.0", Reason: "outside the source's tags"},
		{Tag: "v2.0.0", Package: "com.example.base", Version: "2.0.0", Reason: "no package.json in Packages/Base"},
		{Tag: "v2.0.0", Package: "com.example.pkg", Version: "2.0.0", Reason: "no package.json in Packages/Pkg"},
		{Tag: "v1.1.0", Package: "com.example.pkg", Version: "1.1.0", Reason: "outside the package's versions"},
	} {
		if !slices.Contains(s.Skipped, want) {
			t.Errorf("skipped has no %+v: %+v", want, s.Skipped)
		}
	}
	if len(s.Skipped) != 5 {
		t.Errorf("skipped = %+v", s.Skipped)
	}
	// Nothing is kept: the recipe is not added and has no version list.
	if fsutil.Exists(filepath.Join(f.app.Layout.Recipes(), "mono.json")) || fsutil.Exists(f.app.Layout.Index("mono")) {
		t.Error("probe kept the recipe or a version list")
	}

	// An added recipe is probed by its id.
	if _, err := f.app.Add(ctx, p); err != nil {
		t.Fatal(err, f.log)
	}
	if res, err := f.app.Probe(ctx, "mono"); err != nil || res.Recipe != "mono" || len(res.Sources[0].Skipped) != 5 {
		t.Errorf("probe mono = %+v, %v", res, err)
	}
	if _, err := f.app.Probe(ctx, "nope"); err == nil {
		t.Error("probing no recipe and no file did not fail")
	}
}
