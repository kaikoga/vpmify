package convert

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/kaikoga/vpmify/internal/recipe"
	"github.com/kaikoga/vpmify/internal/testutil"
)

func writeArchive(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.tgz")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExtractStripsTopFolder(t *testing.T) {
	archive := writeArchive(t, testutil.TarGz(map[string]string{
		"package/package.json":      `{"name":"a","version":"1.0.0"}`,
		"package/Runtime/A.cs":      "class A {}",
		"package/Runtime/A.cs.meta": "guid",
		"package/Editor/Sub/B.cs":   "class B {}",
		"package/package.json.meta": "guid",
		"package":                   "", // a stray top-level file is dropped
	}))
	dest := filepath.Join(t.TempDir(), "out")
	if _, err := ExtractTarGz(archive, dest, "", MaxPackageSize); err != nil {
		t.Fatal(err)
	}
	want := []string{"Editor/Sub/B.cs", "Runtime/A.cs", "Runtime/A.cs.meta", "package.json", "package.json.meta"}
	if got := listFiles(t, dest); !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

func TestExtractSubpath(t *testing.T) {
	archive := writeArchive(t, testutil.TarGz(map[string]string{
		"repo-abc/README.md":                     "root",
		"repo-abc/Packages/VRM/package.json":     `{}`,
		"repo-abc/Packages/VRM/Runtime/V.cs":     "",
		"repo-abc/Packages/VRM10/package.json":   `{}`,
		"repo-abc/Packages/UniGLTF/package.json": `{}`,
	}))
	dest := filepath.Join(t.TempDir(), "out")
	if _, err := ExtractTarGz(archive, dest, "/Packages/VRM/", MaxPackageSize); err != nil {
		t.Fatal(err)
	}
	want := []string{"Runtime/V.cs", "package.json"}
	if got := listFiles(t, dest); !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

func TestExtractRejectsEscape(t *testing.T) {
	archive := writeArchive(t, testutil.TarGz(map[string]string{
		"package/../../evil": "x",
	}))
	if _, err := ExtractTarGz(archive, filepath.Join(t.TempDir(), "out"), "", MaxPackageSize); err == nil {
		t.Error("no error for an entry outside the package")
	}
}

func TestExtractRejectsBackslashEscape(t *testing.T) {
	archive := writeArchive(t, testutil.TarGz(map[string]string{
		`package/a\..\..\..\evil.txt`: "x",
	}))
	root := t.TempDir()
	dest := filepath.Join(root, "work", "build", "package")
	_, err := ExtractTarGz(archive, dest, "", MaxPackageSize)
	if runtime.GOOS == "windows" && err == nil {
		t.Error("no error for an entry that leaves the package through backslashes")
	}
	for _, f := range listFiles(t, root) {
		if !strings.HasPrefix(f, "work/build/package/") {
			t.Errorf("wrote %s outside the package", f)
		}
	}
}

func TestExtractStopsAtLimit(t *testing.T) {
	archive := writeArchive(t, testutil.TarGz(map[string]string{
		"package/a.txt": "12345",
		"package/b.txt": "67890",
	}))
	res, err := ExtractTarGz(archive, filepath.Join(t.TempDir(), "out"), "", 10)
	if err != nil || res.Written != 10 {
		t.Errorf("at the limit: written = %d, err = %v", res.Written, err)
	}
	if _, err := ExtractTarGz(archive, filepath.Join(t.TempDir(), "out"), "", 9); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over the limit: err = %v", err)
	}
}

func TestExtractSkipsLinks(t *testing.T) {
	archive := writeArchive(t, testutil.TarGzEntries([]testutil.Entry{
		{Name: "package/package.json", Body: "{}"},
		{Name: "package/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "package/tool.sh", Body: "#!/bin/sh", Mode: 0o755},
	}))
	dest := filepath.Join(t.TempDir(), "out")
	res, err := ExtractTarGz(archive, dest, "", MaxPackageSize)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Skipped, []string{"link"}) {
		t.Errorf("skipped = %v", res.Skipped)
	}
	if _, err := os.Lstat(filepath.Join(dest, "link")); !os.IsNotExist(err) {
		t.Errorf("link was extracted: %v", err)
	}
	info, err := os.Stat(filepath.Join(dest, "tool.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("tool.sh lost its executable bit: %v", info.Mode())
	}
}

func TestOverridesAndManifest(t *testing.T) {
	dir := t.TempDir()
	orig := "{\n  \"name\": \"a\",\n  \"version\": \"3.4.0\",\n  \"unity\": \"2021.3\",\n  \"unityRelease\": null,\n  \"description\": \"d\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckManifest(dir, "a", "3.4.0"); err != nil {
		t.Fatal(err)
	}
	if err := CheckManifest(dir, "a", "3.3.0"); err == nil {
		t.Error("version mismatch not detected")
	}
	if err := CheckManifest(dir, "b", "3.4.0"); err == nil {
		t.Error("name mismatch not detected")
	}
	edits := []recipe.Edit{
		{Set: map[string]json.RawMessage{"unity": json.RawMessage(`"6000.2"`), "unityRelease": json.RawMessage(`"6f2"`)}},
		{Set: map[string]json.RawMessage{"description": json.RawMessage(`null`), "vpmDependencies": json.RawMessage(`{"b":"1.0.0"}`)}},
	}
	if err := ApplyOverrides(dir, edits); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "package.json"))
	want := "{\n  \"name\": \"a\",\n  \"version\": \"3.4.0\",\n  \"unity\": \"6000.2\",\n  \"unityRelease\": \"6f2\",\n  \"vpmDependencies\": {\n    \"b\": \"1.0.0\"\n  }\n}\n"
	if string(got) != want {
		t.Errorf("package.json =\n%s\nwant\n%s", got, want)
	}
	if !strings.HasSuffix(string(got), "}\n") {
		t.Error("trailing newline lost")
	}
}

// merge adds keys to an object and keeps the others, makes the field when
// there is none or null, and comes after the set of its own override.
func TestOverridesMerge(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	keys := func(pairs ...string) map[string]json.RawMessage {
		m := map[string]json.RawMessage{}
		for i := 0; i < len(pairs); i += 2 {
			m[pairs[i]] = raw(`"` + pairs[i+1] + `"`)
		}
		return m
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "package.json")
	orig := `{"name": "a", "version": "1.0.0", "dependencies": {"z": "1.0.0", "b": "1.0.0"}, "legacyFolders": null, "keywords": ["k"]}`
	if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	edits := []recipe.Edit{
		{Merge: map[string]map[string]json.RawMessage{
			"dependencies":    keys("a", "2.0.0", "b", "2.0.0"),
			"vpmDependencies": keys("c", ">=3.0.0"),
			"legacyFolders":   keys("Assets/A", "guid"),
		}},
		{
			Set:   map[string]json.RawMessage{"vpmDependencies": raw(`{"d":"4.0.0"}`)},
			Merge: map[string]map[string]json.RawMessage{"vpmDependencies": keys("e", "5.0.0")},
		},
	}
	if err := ApplyOverrides(dir, edits); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	want := `{
  "name": "a",
  "version": "1.0.0",
  "dependencies": {
    "z": "1.0.0",
    "b": "2.0.0",
    "a": "2.0.0"
  },
  "legacyFolders": {
    "Assets/A": "guid"
  },
  "keywords": [
    "k"
  ],
  "vpmDependencies": {
    "d": "4.0.0",
    "e": "5.0.0"
  }
}`
	if string(got) != want {
		t.Errorf("package.json =\n%s\nwant\n%s", got, want)
	}

	err := ApplyOverrides(dir, []recipe.Edit{{Merge: map[string]map[string]json.RawMessage{"keywords": keys("a", "b")}}})
	if err == nil || !strings.Contains(err.Error(), "keywords is not an object") {
		t.Errorf("merge into an array: err = %v", err)
	}

	// null removes a key, and makes no field where there is none.
	null := map[string]json.RawMessage{"z": raw(`null`), "nowhere": raw(`null`)}
	if err := ApplyOverrides(dir, []recipe.Edit{{Merge: map[string]map[string]json.RawMessage{"dependencies": null, "samples": null}}}); err != nil {
		t.Fatal(err)
	}
	var m struct {
		Dependencies map[string]string `json:"dependencies"`
		Samples      json.RawMessage   `json:"samples"`
	}
	got, _ = os.ReadFile(p)
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"b": "2.0.0", "a": "2.0.0"}; !reflect.DeepEqual(m.Dependencies, want) || m.Samples != nil {
		t.Errorf("dependencies = %v, want %v; samples = %s", m.Dependencies, want, m.Samples)
	}
}
