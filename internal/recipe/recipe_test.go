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
	return `{"id": "pkg", "sources": [{` + gitSourceHead + `, "packages": [{"name": "com.example.pkg"` + pkg + `}]}]}`
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
}

func TestParseRejects(t *testing.T) {
	npm := func(pkgs string) string {
		return `{"id": "pkg", "sources": [{"type": "npm", "registry": "https://r", "packages": [` + pkgs + `]}]}`
	}
	sameTag := func(name string) string {
		return `{"id": "pkg", "sources": [{` + gitSourceHead + `, "packages": [{"name": "a"},
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
		{"no sources", `{"id": "pkg", "sources": []}`, "no sources"},
		{"no packages", `{"id": "pkg", "sources": [{` + gitSourceHead + `, "packages": []}]}`, "no packages"},
		{"twice in a recipe", `{"id": "pkg", "sources": [{` + gitSourceHead + `, "packages": [{"name": "p"}]},
  {"type": "npm", "registry": "https://r", "packages": [{"name": "p"}]}]}`, "comes twice"},
		{"old form", `{"id": "pkg", "source": {"type": "npm", "registry": "https://r", "name": "p"}}`, `under "sources"`},
		{"sameTag on another source", `{"id": "pkg", "sources": [{` + gitSourceHead + `, "packages": [{"name": "a"}]},
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
		if want := strings.TrimSuffix(filepath.Base(f), ".json"); r.ID != want {
			t.Errorf("%s: id %q", f, r.ID)
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
	if key(`, "path": "A", "prerelease": true, "overrides": [{"set": {"unity": "2022.3"}}]`) != base {
		t.Error("prerelease or overrides changed the key")
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
	// So does another package in the source: update reads it too.
	two := mustParse(t, `{"id": "pkg", "sources": [{`+gitSourceHead+`, "packages": [{"name": "com.example.pkg", "path": "A"}, {"name": "b"}]}]}`)
	if two.Sources[0].ListingKey() == base {
		t.Error("another package kept the key")
	}
}

func TestExpandedOverrides(t *testing.T) {
	r := mustParse(t, `{"id": "pkg", "sources": [{`+gitSourceHead+`, "packages": [{"name": "a"}, {"name": "c"},
  {"name": "com.example.pkg", "overrides": [
    {"set": {"displayName": "{version} stays", "vpmDependencies": {"a": ">={version}", "b": "={dependencies}", "c": ">={sameTag}"}}},
    {"versions": ">=2.0.0", "set": {"vpmDependencies": null}}
  ]}]}]}`)
	_, p := r.Package("com.example.pkg")
	sets, err := p.ExpandedOverridesFor(version(t, "1.2.0"), map[string]string{"b": "0.3.0"}, map[string]string{"c": "4.5.6"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || string(sets[0]["vpmDependencies"]) != `{"a":">=1.2.0","b":"=0.3.0","c":">=4.5.6"}` ||
		string(sets[0]["displayName"]) != `"{version} stays"` {
		t.Errorf("sets = %s", sets)
	}
	// The recipe's own sets are left as written.
	if raw := p.OverridesFor(version(t, "1.2.0"))[0]["vpmDependencies"]; !strings.Contains(string(raw), "{version}") {
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
	r := mustParse(t, `{"id": "pkg", "sources": [{`+gitSourceHead+`, "packages": [{"name": "a"}, {"name": "b"}]}]}`)
	s := r.Sources[0]
	_, a := r.Package("a")
	base := s.Fingerprint(a, "A", "commit1", nil, nil)
	if s.Fingerprint(a, "A", "commit2", nil, nil) == base {
		t.Error("another archive kept the fingerprint")
	}
	if s.Fingerprint(a, "A", "commit1", []map[string]json.RawMessage{{"vpmDependencies": json.RawMessage(`{"b":"=1.0.0"}`)}}, nil) == base {
		t.Error("another expanded {sameTag} kept the fingerprint")
	}
	// Another package of the source does not change it.
	other := mustParse(t, `{"id": "pkg", "sources": [{`+gitSourceHead+`, "packages": [{"name": "a"}, {"name": "b", "path": "B"}]}]}`)
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
		{"short commit", with(overlay("", "Sub", "1111111111")), "40 lowercase hex"},
		{"resolve", with(`{"path": "Sub", "resolve": "submodule"}`), "not supported yet"},
		{"bad tags", with(overlay("nonsense", "Sub", commitA)), "overlays[0].tags"},
		{"same folder", with(overlay(">=1.0.0", "Sub", commitA), overlay("<=1.0.0", "Sub", commitB)), "both fill"},
		{"nested folder", with(overlay("", "Sub", commitA), overlay("", "Sub/Inner", commitB)), "both fill"},
		{"npm", `{"id": "pkg", "sources": [{"type": "npm", "registry": "https://r", "packages": [{"name": "p", "overlays": [` + overlay("", "Sub", commitA) + `]}]}]}`, "take no path, paths, versionFromTag or overlays"},
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
