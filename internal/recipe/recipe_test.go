package recipe

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaikoga/vpmify/internal/semver"
)

const gitSourceHead = `"type": "github", "url": "https://github.com/owner/repo"`

// gitRecipe returns a recipe with one GitHub source whose one package,
// com.example.pkg, has the fields in pkg (each starting with ", ").
func gitRecipe(pkg string) string {
	return `{"vpmifyFormat": 1, "id": "pkg", "sources": [{` + gitSourceHead + `, "packages": [{"name": "com.example.pkg"` + pkg + `}]}]}`
}

func mustParse(t *testing.T, s string) *Recipe {
	t.Helper()
	r, err := Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func onlyPackage(t *testing.T, s string) (*Source, *Package) {
	t.Helper()
	r := mustParse(t, s)
	return r.Sources[0], r.Sources[0].Packages[0]
}

func version(t *testing.T, s string) semver.Version {
	t.Helper()
	v, err := semver.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPathFor(t *testing.T) {
	_, p := onlyPackage(t, gitRecipe(`,
  "paths": [{"tags": ">=0.131.0", "path": "Packages/Pkg"}, {"tags": ">=0.100.0 <0.131.0", "path": "Assets/Pkg"}]`))
	cases := []struct {
		tag, want string
		ok        bool
	}{
		{"0.131.0", "Packages/Pkg", true},
		{"0.130.1", "Assets/Pkg", true},
		{"0.99.0", "", false},
	}
	for _, c := range cases {
		if got, ok := p.PathFor(version(t, c.tag)); got != c.want || ok != c.ok {
			t.Errorf("PathFor(%s) = %q %v, want %q %v", c.tag, got, ok, c.want, c.ok)
		}
	}

	_, one := onlyPackage(t, gitRecipe(`, "path": "Packages/Pkg"`))
	if got, ok := one.PathFor(version(t, "1.0.0")); got != "Packages/Pkg" || !ok {
		t.Errorf("path: PathFor = %q %v", got, ok)
	}
	_, root := onlyPackage(t, gitRecipe(``))
	if got, ok := root.PathFor(version(t, "1.0.0")); got != "" || !ok {
		t.Errorf("root: PathFor = %q %v", got, ok)
	}
	_, rootRule := onlyPackage(t, gitRecipe(`, "paths": [{"tags": ">=1.0.0", "path": ""}]`))
	if got, ok := rootRule.PathFor(version(t, "1.0.0")); got != "" || !ok {
		t.Errorf(`"path": "": PathFor = %q %v`, got, ok)
	}

	// Slashes around a folder are dropped, and the listing key with them.
	plain, _ := onlyPackage(t, gitRecipe(`, "path": "Packages/Pkg"`))
	slashed, sp := onlyPackage(t, gitRecipe(`, "path": "/Packages/Pkg/"`))
	if got, _ := sp.PathFor(version(t, "1.0.0")); got != "Packages/Pkg" || slashed.ListingKey() != plain.ListingKey() {
		t.Errorf("slashed path: PathFor = %q, same key = %v", got, slashed.ListingKey() == plain.ListingKey())
	}
	plain, _ = onlyPackage(t, gitRecipe(`, "paths": [{"path": "Packages/Pkg"}]`))
	slashed, sp = onlyPackage(t, gitRecipe(`, "paths": [{"path": "/Packages/Pkg/"}]`))
	if got, _ := sp.PathFor(version(t, "1.0.0")); got != "Packages/Pkg" || slashed.ListingKey() != plain.ListingKey() {
		t.Errorf("slashed paths: PathFor = %q, same key = %v", got, slashed.ListingKey() == plain.ListingKey())
	}
}

func TestParseRejects(t *testing.T) {
	npm := func(pkgs string) string {
		return `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "npm", "registry": "https://r", "packages": [` + pkgs + `]}]}`
	}
	sameTag := func(name string) string {
		return `{"vpmifyFormat": 1, "id": "pkg", "sources": [{` + gitSourceHead + `, "packages": [{"name": "a"},
  {"name": "b", "overrides": [{"set": {"vpmDependencies": {"` + name + `": "={sameTag}"}}}]}]}]}`
	}
	cases := []struct {
		name, recipe, want string
	}{
		{"overlapping paths", gitRecipe(`,
  "paths": [{"tags": ">=1.0.0", "path": "A"}, {"tags": "<=1.0.0", "path": "B"}]`), "overlaps"},
		{"path and paths", gitRecipe(`, "path": "A", "paths": [{"path": "B"}]`), "either path or paths"},
		{"bad folder", gitRecipe(`, "paths": [{"path": "../A"}]`), "bad folder"},
		{"paths without path", gitRecipe(`, "paths": [{"tags": ">=1.0.0"}]`), `has no "path"`},
		{"npm with path", npm(`{"name": "p", "path": "A"}`), "take no path"},
		{"no sources", `{"vpmifyFormat": 1, "id": "pkg", "sources": []}`, "no sources"},
		// Where a source comes from is checked when the recipe is read, so
		// that add saves no recipe update cannot read.
		{"source elsewhere than GitHub", `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "github", "url": "https://gitlab.com/owner/repo", "packages": [{"name": "p"}]}]}`, "want https://github.com/<owner>/<repo>"},
		{"registry that is no URL", `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "npm", "registry": "ftp://r", "packages": [{"name": "p"}]}]}`, "not an https URL"},
		{"registry with the scheme in capitals", `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "npm", "registry": "HTTPS://r", "packages": [{"name": "p"}]}]}`, "not an https URL"},
		{"registry over plain http", `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "npm", "registry": "http://r", "packages": [{"name": "p"}]}]}`, "not an https URL"},
		{"no packages", `{"vpmifyFormat": 1, "id": "pkg", "sources": [{` + gitSourceHead + `, "packages": []}]}`, "no packages"},
		{"twice in a recipe", `{"vpmifyFormat": 1, "id": "pkg", "sources": [{` + gitSourceHead + `, "packages": [{"name": "p"}]},
  {"type": "npm", "registry": "https://r", "packages": [{"name": "p"}]}]}`, "comes twice"},
		{"no format", `{"id": "pkg", "sources": [{"type": "npm", "registry": "https://r", "packages": [{"name": "p"}]}]}`, "not a Vpmify recipe"},
		{"not an object", `[]`, "not a Vpmify recipe"},
		{"format that is no number", `{"vpmifyFormat": "1", "id": "pkg", "sources": []}`, "whole number"},
		{"format zero", `{"vpmifyFormat": 0, "id": "pkg", "sources": []}`, "not a format"},
		// A newer format is refused before anything else is read, so the
		// recipe's writer may have used what this Vpmify would get wrong.
		{"newer format", `{"vpmifyFormat": 2, "id": "PKG", "sources": 3}`, "a newer Vpmify is needed"},
		{"sameTag on another source", `{"vpmifyFormat": 1, "id": "pkg", "sources": [{` + gitSourceHead + `, "packages": [{"name": "a"}]},
  {` + gitSourceHead + `, "packages": [{"name": "b", "overrides": [{"set": {"vpmDependencies": {"a": "={sameTag}"}}}]}]}]}`, "same source"},
		{"sameTag on itself", sameTag("b"), "itself"},
		{"sameTag on npm", npm(`{"name": "a"}, {"name": "b", "overrides": [{"set": {"vpmDependencies": {"a": "={sameTag}"}}}]}`), "needs a source with tags"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.recipe))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
	if _, err := Parse([]byte(sameTag("a"))); err != nil {
		t.Errorf("sameTag on a package of the same source: %v", err)
	}
}

// Fields Vpmify does not know are ignored at every level.
func TestParseIgnoresUnknownFields(t *testing.T) {
	r, err := Parse([]byte(`{"$schema": "https://example.com/recipe.schema.json", "vpmifyFormat": 1, "id": "pkg", "comment": "why",
  "sources": [{` + gitSourceHead + `, "comment": 1, "packages": [{"name": "p", "comment": [], "versionFromTag": {"tags": "<1.0.0", "comment": ""},
    "paths": [{"path": "A", "comment": ""}], "overrides": [{"set": {"unity": "2022.3"}, "comment": ""}],
    "overlays": [{"path": "Sub", "url": "https://github.com/owner/sub", "commit": "` + commitA + `", "comment": ""}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if p := r.Sources[0].Packages[0]; !p.TakesTagVersion(version(t, "0.9.0")) || len(p.OverridesFor(version(t, "1.0.0"))) != 1 {
		t.Errorf("package = %+v", p)
	}
}

// A key is a field only when it is spelled as the field exactly; one that
// differs in case is ignored as unknown. A key that comes twice in an object
// is refused.
func TestParseMatchesKeysExactly(t *testing.T) {
	_, p := onlyPackage(t, gitRecipe(`, "versions": ">=1.0.0", "Versions": ">=2.0.0", "OnDemand": true, "Path": "A",
  "overrides": [{"set": {"Description": "a < b"}}]`))
	if p.Versions != ">=1.0.0" || p.OnDemand || p.Path != "" {
		t.Errorf("versions = %q, onDemand = %v, path = %q", p.Versions, p.OnDemand, p.Path)
	}
	// The keys of set are not fields but package.json's, and keep their case
	// and their values as written.
	if len(p.Overrides) != 1 || string(p.Overrides[0].Set["Description"]) != `"a < b"` {
		t.Errorf("overrides = %+v", p.Overrides)
	}

	for _, c := range []struct{ name, recipe, want string }{
		{"format", `{"VpmifyFormat": 1, "id": "pkg", "sources": [{"type": "npm", "registry": "https://r", "packages": [{"name": "p"}]}]}`, "no vpmifyFormat"},
		{"versionFromTag", gitRecipe(`, "versionFromTag": {"Tags": "<1.0.0"}`), "needs tags"},
		// set in an embedded struct of Override.
		{"set", gitRecipe(`, "overrides": [{"Set": {"unity": "2022.3"}}]`), "neither set nor merge"},
		{"source", `{"vpmifyFormat": 1, "id": "pkg", "Sources": [{"type": "npm", "registry": "https://r", "packages": [{"name": "p"}]}]}`, "no sources"},
		{"twice", gitRecipe(`, "versions": ">=1.0.0", "versions": ">=2.0.0"`), "duplicate"},
		{"twice in set", gitRecipe(`, "overrides": [{"set": {"unity": "2022.3", "unity": "2019.4"}}]`), "duplicate"},
		{"format twice", `{"vpmifyFormat": 1, "vpmifyFormat": 1, "id": "pkg", "sources": []}`, "duplicate"},
	} {
		if _, err := Parse([]byte(c.recipe)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
}

func TestGitHubRepo(t *testing.T) {
	for _, ok := range []string{"https://github.com/a/b", "https://github.com/a/b.git", "https://github.com/a/b/"} {
		if o, r, err := GitHubRepo(ok); err != nil || o != "a" || r != "b" {
			t.Errorf("%s: %s %s %v", ok, o, r, err)
		}
	}
	for _, bad := range []string{"http://github.com/a/b", "https://gitlab.com/a/b", "https://github.com/a", "https://github.com/a/b/c", "git@github.com:a/b.git"} {
		if _, _, err := GitHubRepo(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestParseTakesByteOrderMark(t *testing.T) {
	if r, err := Parse([]byte("\xef\xbb\xbf" + gitRecipe(``))); err != nil || r.ID != "pkg" {
		t.Errorf("recipe = %+v, err = %v", r, err)
	}
}

func TestExamplesParse(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "examples", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("examples: %v %v", files, err)
	}
	for _, f := range files {
		r, err := Load(f)
		if err != nil {
			t.Error(err)
			continue
		}
		// The examples are kaikoga's recipes, named after their files.
		if want := "net.kaikoga.vpmify." + strings.TrimSuffix(filepath.Base(f), ".json"); r.ID != want || r.DisplayName == "" {
			t.Errorf("%s: id %q, displayName %q", f, r.ID, r.DisplayName)
		}
	}
}

func TestListingKey(t *testing.T) {
	key := func(pkg string) string {
		s, _ := onlyPackage(t, gitRecipe(pkg))
		return s.ListingKey()
	}
	base := key(`, "path": "A"`)
	// What is applied to the list when it is read does not change the key.
	if key(`, "path": "A", "overrides": [{"set": {"unity": "2022.3"}}]`) != base {
		t.Error("overrides changed the key")
	}
	// What update applies when it reads upstream does.
	if key(`, "path": "B"`) == base {
		t.Error("another folder kept the key")
	}
	if key(`, "path": "A", "versions": ">=1.0.0"`) == base {
		t.Error("another range kept the key")
	}
	if key(`, "path": "A", "overlays": [`+overlay("", "Sub", commitA)+`]`) == base {
		t.Error("an overlay kept the key")
	}
	if key(`, "path": "A", "overlays": []`) != base {
		t.Error("no overlays changed the key")
	}
	// Slashes around an overlay's folder are dropped in the key too.
	if key(`, "path": "A", "overlays": [`+overlay("", "/Sub/", commitA)+`]`) != key(`, "path": "A", "overlays": [`+overlay("", "Sub", commitA)+`]`) {
		t.Error("slashes around an overlay's path changed the key")
	}
	// So does another package in the source: update reads it too.
	two := mustParse(t, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{`+gitSourceHead+`, "packages": [{"name": "com.example.pkg", "path": "A"}, {"name": "b"}]}]}`)
	if two.Sources[0].ListingKey() == base {
		t.Error("another package kept the key")
	}
}

func TestExpandedOverrides(t *testing.T) {
	r := mustParse(t, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{`+gitSourceHead+`, "packages": [{"name": "a"}, {"name": "c"},
  {"name": "com.example.pkg", "overrides": [
    {"set": {"displayName": "{version} stays", "vpmDependencies": {"a": ">={version}", "b": "={dependencies}", "c": ">={sameTag}"}}},
    {"versions": ">=2.0.0", "set": {"vpmDependencies": null}}
  ]}]}]}`)
	_, p := r.Package("com.example.pkg")
	sets, err := p.ExpandedOverridesFor(version(t, "1.2.0"), map[string]string{"b": "0.3.0"}, map[string]string{"c": "4.5.6"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || string(sets[0].Set["vpmDependencies"]) != `{"a":">=1.2.0","b":"=0.3.0","c":">=4.5.6"}` ||
		string(sets[0].Set["displayName"]) != `"{version} stays"` {
		t.Errorf("sets = %s", sets)
	}
	// The recipe's own sets are left as written.
	if raw := p.OverridesFor(version(t, "1.2.0"))[0].Set["vpmDependencies"]; !strings.Contains(string(raw), "{version}") {
		t.Errorf("OverridesFor changed: %s", raw)
	}
	if _, err := p.ExpandedOverridesFor(version(t, "1.2.0"), nil, map[string]string{"c": "4.5.6"}); err == nil || !strings.Contains(err.Error(), "no UPM dependency on b") {
		t.Errorf("err = %v", err)
	}
	if _, err := p.ExpandedOverridesFor(version(t, "1.2.0"), map[string]string{"b": "0.3.0"}, nil); err == nil || !strings.Contains(err.Error(), "same tag has no version of c") {
		t.Errorf("err = %v", err)
	}
	if sets, err := p.ExpandedOverridesFor(version(t, "2.0.0"), nil, nil); err == nil {
		t.Errorf("2.0.0: the first set still applies, so {dependencies} has nothing to read: %s", sets)
	}

	// {dependencies} reads "dependencies" as the overrides leave it, wherever
	// they set it: a later override corrects b for 1.x, and removes the field
	// for 3.x.
	_, p = onlyPackage(t, gitRecipe(`, "overrides": [
    {"set": {"vpmDependencies": {"b": "={dependencies}"}}},
    {"versions": "<2.0.0", "set": {"dependencies": {"b": "0.3.2"}}},
    {"versions": ">=3.0.0", "set": {"dependencies": null}}
  ]`))
	for _, c := range []struct{ version, want string }{{"1.2.0", "=0.3.2"}, {"2.0.0", "=0.3.0"}} {
		sets, err := p.ExpandedOverridesFor(version(t, c.version), map[string]string{"b": "0.3.0"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(sets[0].Set["vpmDependencies"]); got != `{"b":"`+c.want+`"}` {
			t.Errorf("%s: vpmDependencies = %s, want b %s", c.version, got, c.want)
		}
	}
	if _, err := p.ExpandedOverridesFor(version(t, "3.0.0"), map[string]string{"b": "0.3.0"}, nil); err == nil || !strings.Contains(err.Error(), "no UPM dependency on b") {
		t.Errorf("3.0.0: err = %v", err)
	}

	// merge takes the placeholders in vpmDependencies too, and what it
	// merges into "dependencies" is read by {dependencies}, with upstream's
	// other keys kept.
	_, p = mustParse(t, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{`+gitSourceHead+`, "packages": [{"name": "c"},
  {"name": "com.example.pkg", "overrides": [
    {"merge": {"vpmDependencies": {"a": "={dependencies}", "b": "={dependencies}", "c": ">={sameTag}"}}},
    {"versions": "<2.0.0", "merge": {"dependencies": {"b": "0.3.2"}}}
  ]}]}]}`).Package("com.example.pkg")
	upstream := map[string]string{"a": "0.1.0", "b": "0.3.0"}
	edits, err := p.ExpandedOverridesFor(version(t, "1.2.0"), upstream, map[string]string{"c": "4.5.6"})
	if err != nil {
		t.Fatal(err)
	}
	merged := edits[0].Merge["vpmDependencies"]
	if len(merged) != 3 || string(merged["a"]) != `"=0.1.0"` || string(merged["b"]) != `"=0.3.2"` || string(merged["c"]) != `">=4.5.6"` || upstream["b"] != "0.3.0" {
		t.Errorf("merge vpmDependencies = %s, upstream = %v", merged, upstream)
	}
}

// null in merge removes the key: {dependencies} no longer reads it, and in
// vpmDependencies it goes to the conversion as it is.
func TestMergeNullRemovesTheKey(t *testing.T) {
	_, p := onlyPackage(t, gitRecipe(`, "overrides": [
    {"merge": {"vpmDependencies": {"a": "={dependencies}", "b": null}}},
    {"versions": ">=2.0.0", "merge": {"dependencies": {"a": null}}}
  ]`))
	upstream := map[string]string{"a": "0.1.0"}
	edits, err := p.ExpandedOverridesFor(version(t, "1.0.0"), upstream, nil)
	if err != nil {
		t.Fatal(err)
	}
	merged := edits[0].Merge["vpmDependencies"]
	if string(merged["a"]) != `"=0.1.0"` || string(merged["b"]) != `null` {
		t.Errorf("merge vpmDependencies = %s", merged)
	}
	if _, err := p.ExpandedOverridesFor(version(t, "2.0.0"), upstream, nil); err == nil || !strings.Contains(err.Error(), "no UPM dependency on a") {
		t.Errorf("2.0.0: err = %v", err)
	}
}

func TestMergeRejected(t *testing.T) {
	cases := []struct{ name, overrides, want string }{
		{"neither", `{"versions": ">=1.0.0"}`, "neither set nor merge"},
		{"not an object", `{"merge": {"dependencies": "1.0.0"}}`, "merge"},
		{"version", `{"merge": {"version": {"a": "b"}}}`, `must not merge into "version"`},
		{"unknown placeholder", `{"merge": {"vpmDependencies": {"a": "={self}"}}}`, "merge.vpmDependencies"},
		{"sameTag on itself", `{"merge": {"vpmDependencies": {"com.example.pkg": "={sameTag}"}}}`, "itself"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(gitRecipe(`, "overrides": [` + c.overrides + `]`)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
}

func TestMergeKeepsListingKeyAndChangesFingerprint(t *testing.T) {
	s, p := onlyPackage(t, gitRecipe(`, "overrides": [{"merge": {"dependencies": {"a": "1.0.0"}}}]`))
	plain, _ := onlyPackage(t, gitRecipe(``))
	if s.ListingKey() != plain.ListingKey() {
		t.Error("merge changed the listing key")
	}
	edits, err := p.ExpandedOverridesFor(version(t, "1.0.0"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Fingerprint(p, "", "commit1", edits, nil) == s.Fingerprint(p, "", "commit1", nil, nil) {
		t.Error("merge kept the fingerprint")
	}
	for _, bad := range []string{
		`{"vpmDependencies": {"a": ">={self}"}}`,
		`{"vpmDependencies": ["a"]}`,
	} {
		if _, err := Parse([]byte(gitRecipe(`, "overrides": [{"set": ` + bad + `}]`))); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestFingerprint(t *testing.T) {
	r := mustParse(t, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{`+gitSourceHead+`, "packages": [{"name": "a"}, {"name": "b"}]}]}`)
	s := r.Sources[0]
	_, a := r.Package("a")
	base := s.Fingerprint(a, "A", "commit1", nil, nil)
	if s.Fingerprint(a, "A", "commit2", nil, nil) == base {
		t.Error("another archive kept the fingerprint")
	}
	if s.Fingerprint(a, "A", "commit1", []Edit{{Set: map[string]json.RawMessage{"vpmDependencies": json.RawMessage(`{"b":"=1.0.0"}`)}}}, nil) == base {
		t.Error("another expanded {sameTag} kept the fingerprint")
	}
	// Another package of the source does not change it.
	other := mustParse(t, `{"vpmifyFormat": 1, "id": "pkg", "sources": [{`+gitSourceHead+`, "packages": [{"name": "a"}, {"name": "b", "path": "B"}]}]}`)
	if other.Sources[0].Fingerprint(a, "A", "commit1", nil, nil) != base {
		t.Error("a sibling changed the fingerprint")
	}
	// No overlays is what versions built before overlays came have.
	if s.Fingerprint(a, "A", "commit1", nil, []BuiltOverlay{}) != base {
		t.Error("an empty list of overlays changed the fingerprint")
	}
	overlay := s.Fingerprint(a, "A", "commit1", nil, []BuiltOverlay{{Path: "Sub", Archive: "sub1"}})
	if overlay == base {
		t.Error("an overlay kept the fingerprint")
	}
	if s.Fingerprint(a, "A", "commit1", nil, []BuiltOverlay{{Path: "Sub", Archive: "sub2"}}) == overlay {
		t.Error("another overlay archive kept the fingerprint")
	}
}

const (
	commitA = "1111111111111111111111111111111111111111"
	commitB = "2222222222222222222222222222222222222222"
)

func overlay(tags, path, commit string) string {
	return `{"tags": "` + tags + `", "path": "` + path + `", "url": "https://github.com/owner/sub", "commit": "` + commit + `"}`
}

func TestOverlaysFor(t *testing.T) {
	_, p := onlyPackage(t, gitRecipe(`, "overlays": [`+overlay("<2.0.0", "Sub/", commitA)+`, `+overlay(">=2.0.0", "Lib/Sub", commitB)+`, `+overlay(">=1.0.0", "Other", commitB)+`]`))
	got := p.OverlaysFor(version(t, "1.5.0"))
	if len(got) != 2 || got[0].Path != "Sub" || got[0].Commit != commitA || got[1].Path != "Other" {
		t.Errorf("1.5.0: %+v", got)
	}
	got = p.OverlaysFor(version(t, "2.0.0"))
	if len(got) != 2 || got[0].Path != "Lib/Sub" || got[0].Commit != commitB {
		t.Errorf("2.0.0: %+v", got)
	}
	if got := p.OverlaysFor(version(t, "0.5.0")); len(got) != 1 || got[0].Path != "Sub" {
		t.Errorf("0.5.0: %+v", got)
	}
}

func TestOverlaysRejected(t *testing.T) {
	with := func(overlays ...string) string {
		return gitRecipe(`, "overlays": [` + strings.Join(overlays, ", ") + `]`)
	}
	cases := []struct {
		name, recipe, want string
	}{
		{"no path", with(`{"url": "https://github.com/owner/sub", "commit": "` + commitA + `"}`), `needs a "path"`},
		{"root", with(overlay("", "/", commitA)), `needs a "path"`},
		{"outside", with(overlay("", "../Sub", commitA)), "bad folder"},
		{"no url", with(`{"path": "Sub", "commit": "` + commitA + `"}`), "needs url"},
		{"elsewhere than GitHub", with(`{"path": "Sub", "url": "https://gitlab.com/owner/sub", "commit": "` + commitA + `"}`), "overlays[0].url"},
		{"short commit", with(overlay("", "Sub", "1111111111")), "40 lowercase hex"},
		{"resolve", with(`{"path": "Sub", "resolve": "submodule"}`), "not supported yet"},
		{"bad tags", with(overlay("nonsense", "Sub", commitA)), "overlays[0].tags"},
		{"same folder", with(overlay(">=1.0.0", "Sub", commitA), overlay("<=1.0.0", "Sub", commitB)), "both fill"},
		{"nested folder", with(overlay("", "Sub", commitA), overlay("", "Sub/Inner", commitB)), "both fill"},
		{"npm", `{"vpmifyFormat": 1, "id": "pkg", "sources": [{"type": "npm", "registry": "https://r", "packages": [{"name": "p", "overlays": [` + overlay("", "Sub", commitA) + `]}]}]}`, "take no path, paths, versionFromTag or overlays"},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.recipe))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
	// One folder in tags that do not overlap, and folders side by side.
	for _, ok := range []string{
		with(overlay("<1.0.0", "Sub", commitA), overlay(">=1.0.0", "Sub", commitB)),
		with(overlay("", "Sub", commitA), overlay("", "SubOther", commitB)),
	} {
		if _, err := Parse([]byte(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}
