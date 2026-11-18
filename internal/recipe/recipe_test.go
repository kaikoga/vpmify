package recipe

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kaikoga/vpmify/internal/semver"
)

const gitSource = `"source": {"type": "github", "url": "https://github.com/owner/repo", "name": "com.example.pkg"}`

func mustParse(t *testing.T, s string) *Recipe {
	t.Helper()
	r, err := Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return r
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
	r := mustParse(t, `{"id": "pkg", `+gitSource+`,
  "paths": [{"tags": ">=0.131.0", "path": "Packages/Pkg"}, {"tags": ">=0.100.0 <0.131.0", "path": "Assets/Pkg"}]}`)
	cases := []struct {
		tag, want string
		ok        bool
	}{
		{"0.131.0", "Packages/Pkg", true},
		{"0.130.1", "Assets/Pkg", true},
		{"0.99.0", "", false},
	}
	for _, c := range cases {
		if got, ok := r.PathFor(version(t, c.tag)); got != c.want || ok != c.ok {
			t.Errorf("PathFor(%s) = %q %v, want %q %v", c.tag, got, ok, c.want, c.ok)
		}
	}

	one := mustParse(t, `{"id": "pkg", `+gitSource+`, "path": "Packages/Pkg"}`)
	if got, ok := one.PathFor(version(t, "1.0.0")); got != "Packages/Pkg" || !ok {
		t.Errorf("path: PathFor = %q %v", got, ok)
	}
	root := mustParse(t, `{"id": "pkg", `+gitSource+`}`)
	if got, ok := root.PathFor(version(t, "1.0.0")); got != "" || !ok {
		t.Errorf("root: PathFor = %q %v", got, ok)
	}
	rootRule := mustParse(t, `{"id": "pkg", `+gitSource+`, "paths": [{"tags": ">=1.0.0", "path": ""}]}`)
	if got, ok := rootRule.PathFor(version(t, "1.0.0")); got != "" || !ok {
		t.Errorf(`"path": "": PathFor = %q %v`, got, ok)
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name, recipe, want string
	}{
		{"overlapping paths", `{"id": "pkg", ` + gitSource + `,
  "paths": [{"tags": ">=1.0.0", "path": "A"}, {"tags": "<=1.0.0", "path": "B"}]}`, "overlaps"},
		{"path and paths", `{"id": "pkg", ` + gitSource + `, "path": "A", "paths": [{"path": "B"}]}`, "either path or paths"},
		{"bad folder", `{"id": "pkg", ` + gitSource + `, "paths": [{"path": "../A"}]}`, "bad folder"},
		{"paths without path", `{"id": "pkg", ` + gitSource + `, "paths": [{"tags": ">=1.0.0"}]}`, `has no "path"`},
		{"npm with path", `{"id": "pkg", "source": {"type": "npm", "registry": "https://r", "name": "p"}, "path": "A"}`, "npm source takes no"},
		{"old select", `{"id": "pkg", ` + gitSource + `, "select": "all"}`, "pull --all"},
		{"old paths", `{"id": "pkg", ` + gitSource + `, "paths": ["A", "B"]}`, `"paths": [{"tags"`},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.recipe))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
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
	base := mustParse(t, `{"id": "pkg", `+gitSource+`, "path": "A"}`)
	// What is applied to the list when it is read does not change the key.
	same := mustParse(t, `{"id": "pkg", `+gitSource+`, "path": "A", "prerelease": true,
  "overrides": [{"set": {"unity": "2022.3"}}]}`)
	if base.ListingKey() != same.ListingKey() {
		t.Error("prerelease or overrides changed the key")
	}
	// What update applies when it reads upstream does.
	moved := mustParse(t, `{"id": "pkg", `+gitSource+`, "path": "B"}`)
	if base.ListingKey() == moved.ListingKey() {
		t.Error("another folder kept the key")
	}
	narrowed := mustParse(t, `{"id": "pkg", `+gitSource+`, "path": "A", "versions": ">=1.0.0"}`)
	if base.ListingKey() == narrowed.ListingKey() {
		t.Error("another range kept the key")
	}
}

func TestExpandedOverrides(t *testing.T) {
	r := mustParse(t, `{"id": "pkg", `+gitSource+`, "overrides": [
  {"set": {"displayName": "{version} stays", "vpmDependencies": {"a": ">={version}", "b": "={dependencies}"}}},
  {"versions": ">=2.0.0", "set": {"vpmDependencies": null}}
]}`)
	sets, err := r.ExpandedOverridesFor(version(t, "1.2.0"), map[string]string{"b": "0.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || string(sets[0]["vpmDependencies"]) != `{"a":">=1.2.0","b":"=0.3.0"}` ||
		string(sets[0]["displayName"]) != `"{version} stays"` {
		t.Errorf("sets = %s", sets)
	}
	// The recipe's own sets are left as written.
	if raw := r.OverridesFor(version(t, "1.2.0"))[0]["vpmDependencies"]; !strings.Contains(string(raw), "{version}") {
		t.Errorf("OverridesFor changed: %s", raw)
	}
	if _, err := r.ExpandedOverridesFor(version(t, "1.2.0"), nil); err == nil || !strings.Contains(err.Error(), "no UPM dependency on b") {
		t.Errorf("err = %v", err)
	}
	if sets, err := r.ExpandedOverridesFor(version(t, "2.0.0"), nil); err == nil {
		t.Errorf("2.0.0: the first set still applies, so {dependencies} has nothing to read: %s", sets)
	}

	for _, bad := range []string{
		`{"vpmDependencies": {"a": ">={self}"}}`,
		`{"vpmDependencies": ["a"]}`,
	} {
		if _, err := Parse([]byte(`{"id": "pkg", ` + gitSource + `, "overrides": [{"set": ` + bad + `}]}`)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
