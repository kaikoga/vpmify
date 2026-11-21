package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/kaikoga/vpmify/internal/alcom"
	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/paths"
	"github.com/kaikoga/vpmify/internal/testutil"
)

const pkgName = "com.example.kit"

const registryURL = "https://registry.test"

// registry is a fake npm registry for one package. It answers through the
// client's transport, so tests open no port.
type registry struct {
	mu        sync.Mutex
	versions  map[string]string // version -> unity
	tarballs  map[string][]byte
	downloads map[string]int
	// handler serves requests; tests may replace it.
	handler http.HandlerFunc
	down    bool
}

func newRegistry() *registry {
	r := &registry{versions: map[string]string{}, tarballs: map[string][]byte{}, downloads: map[string]int{}}
	r.handler = r.serve
	return r
}

func (r *registry) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.down {
		return nil, errors.New("registry is down")
	}
	rec := httptest.NewRecorder()
	r.handler(rec, req)
	return rec.Result(), nil
}

func (r *registry) add(version, unity string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.versions[version] = unity
	manifest := fmt.Sprintf("{\n  \"name\": %q,\n  \"version\": %q,\n  \"unity\": %q,\n  \"unityRelease\": null\n}\n", pkgName, version, unity)
	r.tarballs[version] = testutil.TarGz(map[string]string{
		"package/package.json":      manifest,
		"package/package.json.meta": "guid",
		"package/Runtime/Kit.cs":    "class Kit {}",
	})
}

func (r *registry) serve(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.URL.Path == "/"+pkgName {
		versions := map[string]any{}
		for v, unity := range r.versions {
			versions[v] = map[string]any{
				"name": pkgName, "version": v, "unity": unity,
				"dist": map[string]any{
					"tarball":   registryURL + "/" + pkgName + "/-/" + v + ".tgz",
					"integrity": testutil.Integrity(r.tarballs[v]),
				},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": pkgName, "versions": versions})
		return
	}
	if v, ok := strings.CutPrefix(req.URL.Path, "/"+pkgName+"/-/"); ok {
		v = strings.TrimSuffix(v, ".tgz")
		if data, ok := r.tarballs[v]; ok {
			r.downloads[v]++
			_, _ = w.Write(data)
			return
		}
	}
	http.NotFound(w, req)
}

type fixture struct {
	t        *testing.T
	app      *App
	reg      *registry
	settings string
	foreign  string
	log      *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	base := t.TempDir()
	layout := paths.Layout{Base: base}
	foreign := filepath.Join(base, "hand-made", "pkg")
	settings := layout.ALCOMSettings()
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"pathToUnityExe": "", "userPackageFolders": [` + jsonString(foreign) + `], "defaultProjectPath": null}`
	if err := os.WriteFile(settings, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	log := &bytes.Buffer{}
	reg := newRegistry()
	client := fetch.NewClient("vpmify-test")
	client.HTTP.Transport = reg
	return &fixture{
		t:        t,
		app:      &App{Layout: layout, Client: client, Log: log},
		reg:      reg,
		settings: settings,
		foreign:  foreign,
		log:      log,
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// npmRecipe returns a recipe with one npm source whose one package has the
// fields in extra (each starting with ", ").
func npmRecipe(id, registry, name, extra string) string {
	return `{
  "vpmifyFormat": 1, "displayName": "", "id": "` + id + `",
  "sources": [{"type": "npm", "registry": "` + registry + `", "packages": [{"name": "` + name + `"` + extra + `}]}]
}`
}

// writeRecipe writes recipe kit for the fake registry's package, with the
// package fields in extra.
func (f *fixture) writeRecipe(extra string) string {
	f.t.Helper()
	content := npmRecipe("kit", registryURL, pkgName, extra)
	p := filepath.Join(f.t.TempDir(), "kit.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

// addAndPull adds recipes and pulls, the way a user starts.
func (f *fixture) addAndPull(opt PullOptions, refs ...string) error {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.app.Add(ctx, refs...); err != nil {
		return err
	}
	_, err := f.app.Pull(ctx, nil, opt)
	return err
}

func (f *fixture) available() []string {
	f.t.Helper()
	res, err := f.app.List()
	if err != nil {
		f.t.Fatal(err)
	}
	if res.SettingsError != "" {
		f.t.Fatal(res.SettingsError)
	}
	var out []string
	for _, e := range res.Entries {
		reg := ""
		if !e.Registered {
			reg = " unregistered"
		}
		out = append(out, e.Version+"@"+e.Unity+reg)
	}
	return out
}

func (f *fixture) registered() []string {
	f.t.Helper()
	s, err := alcom.Load(f.settings)
	if err != nil {
		f.t.Fatal(err)
	}
	folders, err := s.UserPackageFolders()
	if err != nil {
		f.t.Fatal(err)
	}
	sort.Strings(folders)
	return folders
}

func (f *fixture) manifest(version string) map[string]any {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.app.Layout.PackageDir("kit", pkgName, version), "package.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		f.t.Fatal(err)
	}
	return m
}

const overrideNewUnity = `,
  "overrides": [{"versions": ">=2.0.0", "set": {"unity": "6000.2", "unityRelease": "6f2"}}]`

func TestPullPlacesNewestAndRegisters(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	f.reg.add("2.0.0", "2021.3")
	f.reg.add("2.1.0-beta.1", "2021.3")
	ctx := context.Background()

	// add reads the version list and places nothing.
	if _, err := f.app.Add(ctx, f.writeRecipe(overrideNewUnity)); err != nil {
		t.Fatal(err, f.log)
	}
	if got := f.available(); len(got) != 0 {
		t.Errorf("placed after add = %v", got)
	}
	if len(f.reg.downloads) != 0 {
		t.Errorf("add downloaded %v", f.reg.downloads)
	}

	// The newest stable version only, though the newest for Unity 2021.3 is
	// another.
	if _, err := f.app.Pull(ctx, nil, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"2.0.0@6000.2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
	wantFolders := []string{
		f.app.Layout.PackageDir("kit", pkgName, "2.0.0"),
		f.foreign,
	}
	sort.Strings(wantFolders)
	if got := f.registered(); !reflect.DeepEqual(got, wantFolders) {
		t.Errorf("registered = %v, want %v", got, wantFolders)
	}
	if m := f.manifest("2.0.0"); m["unity"] != "6000.2" || m["unityRelease"] != "6f2" {
		t.Errorf("2.0.0 manifest = %v", m)
	}
	// A project on Unity 2021.3 needs a version named.
	if _, err := f.app.Pull(ctx, []string{"kit@1.1.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if m := f.manifest("1.1.0"); m["unity"] != "2021.3" || m["unityRelease"] != nil {
		t.Errorf("1.1.0 manifest = %v", m)
	}
	if _, err := os.Stat(filepath.Join(f.app.Layout.PackageDir("kit", pkgName, "2.0.0"), "Runtime", "Kit.cs")); err != nil {
		t.Error(err)
	}
	// Work folders are cleaned up.
	if entries, _ := os.ReadDir(f.app.Layout.Tmp()); len(entries) != 0 {
		t.Errorf("tmp left: %v", entries)
	}
}

func TestPullKeepsOlderVersionsAndAddsNamedOnes(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}

	// A new version upstream is not pulled until the version list is read
	// again.
	f.reg.add("1.2.0", "2021.3")
	if _, err := f.app.Pull(ctx, nil, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed before update = %v, want %v", got, want)
	}
	if _, err := f.app.Update(ctx, nil); err != nil {
		t.Fatal(err, f.log)
	}
	if !strings.Contains(f.log.String(), "new: 1.2.0") {
		t.Errorf("update did not name the new version:\n%s", f.log)
	}
	if _, err := f.app.Pull(ctx, nil, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.1.0@2021.3", "1.2.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}

	if _, err := f.app.Pull(ctx, []string{"kit@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3", "1.1.0@2021.3", "1.2.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}

	if _, err := f.app.Pull(ctx, []string{"kit@9.9.9"}, PullOptions{}); err == nil {
		t.Error("no error for a version the version list does not have")
	}

	// Nothing is downloaded twice.
	for v, n := range f.reg.downloads {
		if n != 1 {
			t.Errorf("%s downloaded %d times", v, n)
		}
	}
}

// TestResults checks what add, update, pull and remove return, which
// `--json` prints.
func TestResults(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	ctx := context.Background()

	added, err := f.app.Add(ctx, f.writeRecipe(""))
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(added.Added) != 1 || added.Added[0].ID != "kit" {
		t.Errorf("added = %+v", added.Added)
	}
	// The first list has nothing to call new.
	if want := []RecipeUpdate{{ID: "kit", Packages: []PackageUpdate{{Package: pkgName, Versions: 2, Upstream: 2, New: []string{}}}, Warnings: []string{}}}; !reflect.DeepEqual(added.Recipes, want) {
		t.Errorf("add read %+v, want %+v", added.Recipes, want)
	}

	pulled, err := f.app.Pull(ctx, nil, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	dir := f.app.Layout.PackageDir("kit", pkgName, "1.1.0")
	wantBuilt := []Built{{Recipe: "kit", Package: pkgName, Version: "1.1.0", Unity: "2021.3", Path: dir, Origin: pulled.Built[0].Origin}}
	if !reflect.DeepEqual(pulled.Built, wantBuilt) || pulled.Built[0].Origin == "" {
		t.Errorf("built = %+v, want %+v with an origin", pulled.Built, wantBuilt)
	}
	if !reflect.DeepEqual(pulled.Registered, []string{dir}) || len(pulled.Unregistered) != 0 || len(pulled.UpToDate) != 0 {
		t.Errorf("pull = %+v", pulled)
	}

	f.reg.add("1.2.0", "2021.3")
	updated, err := f.app.Update(ctx, nil)
	if err != nil {
		t.Fatal(err, f.log)
	}
	if want := []RecipeUpdate{{ID: "kit", Packages: []PackageUpdate{{Package: pkgName, Versions: 3, Upstream: 3, New: []string{"1.2.0"}}}, Warnings: []string{}}}; !reflect.DeepEqual(updated.Recipes, want) {
		t.Errorf("update read %+v, want %+v", updated.Recipes, want)
	}

	// A recipe that fails leaves the others done, and is named in Errors.
	pulled, err = f.app.Pull(ctx, []string{"kit", "nope"}, PullOptions{})
	if err == nil {
		t.Fatal("no error for a recipe the user does not have")
	}
	if len(pulled.Errors) != 1 || pulled.Errors[0].Recipe != "nope" || !strings.Contains(err.Error(), "nope: "+pulled.Errors[0].Error) {
		t.Errorf("errors = %+v, err = %v", pulled.Errors, err)
	}
	if !reflect.DeepEqual(pulled.UpToDate, []string{}) || len(pulled.Built) != 1 || pulled.Built[0].Version != "1.2.0" {
		t.Errorf("pull = %+v", pulled)
	}

	removed, err := f.app.RemoveVersions("kit", "1.1.0")
	if err != nil {
		t.Fatal(err, f.log)
	}
	wantRemoved := []Entry{{Recipe: "kit", Package: pkgName, Version: "1.1.0", Unity: "2021.3", Path: dir}}
	if !reflect.DeepEqual(removed.Removed, wantRemoved) || !removed.Applied || removed.RecipeRemoved || !reflect.DeepEqual(removed.Unregistered, []string{dir}) {
		t.Errorf("remove = %+v", removed)
	}
	// Without apply, RemoveRecipe only tells what it would remove.
	preview, err := f.app.RemoveRecipe("kit", false)
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(preview.Removed) != 1 || preview.Applied || preview.RecipeRemoved {
		t.Errorf("remove kit without apply = %+v", preview)
	}
	if got := f.available(); len(got) != 1 {
		t.Errorf("available after the preview = %v", got)
	}
	removed, err = f.app.RemoveRecipe("kit", true)
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(removed.Removed) != 1 || !removed.Applied || !removed.RecipeRemoved {
		t.Errorf("remove kit = %+v", removed)
	}
}

func TestPullAll(t *testing.T) {
	f := newFixture(t)
	for _, v := range []string{"0.9.0", "1.0.0", "1.1.0", "2.0.0"} {
		f.reg.add(v, "2021.3")
	}
	if err := f.addAndPull(PullOptions{All: true}, f.writeRecipe(`, "versions": ">=1.0.0"`)); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3", "1.1.0@2021.3", "2.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
}

func TestVersions(t *testing.T) {
	f := newFixture(t)
	for _, v := range []string{"0.9.0", "1.0.0", "1.1.0", "2.0.0", "2.1.0-beta.1"} {
		f.reg.add(v, "2021.3")
	}
	ctx := context.Background()
	if _, err := f.app.Add(ctx, f.writeRecipe(`, "versions": ">=1.0.0"`+overrideNewUnity)); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.Pull(ctx, []string{"kit", "kit@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	// The version list keeps only the recipe's range, so 0.9.0 is not taken.
	if _, err := f.app.Pull(ctx, []string{"kit@0.9.0"}, PullOptions{}); err == nil || !strings.Contains(err.Error(), "outside the packages' versions") {
		t.Errorf("pull kit@0.9.0: err = %v", err)
	}
	if _, err := f.app.Pull(ctx, []string{"kit/" + pkgName + "@0.9.0"}, PullOptions{}); err == nil || !strings.Contains(err.Error(), "outside the package's versions") {
		t.Errorf("pull kit@0.9.0: err = %v", err)
	}
	f.reg.down = true // versions reads only the version list

	show := func(opt VersionsOptions) []string {
		t.Helper()
		list, err := f.app.Versions("kit", opt)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, v := range list.Packages[0].Versions {
			out = append(out, fmt.Sprintf("%s %s available=%v", v.Version, v.Unity, v.Available))
		}
		return out
	}
	cases := []struct {
		name string
		opt  VersionsOptions
		want []string
	}{
		{"stable", VersionsOptions{}, []string{
			"2.0.0 6000.2 available=true",
			"1.1.0 2021.3 available=false",
			"1.0.0 2021.3 available=true",
		}},
		{"all", VersionsOptions{All: true}, []string{
			"2.1.0-beta.1 6000.2 available=false",
			"2.0.0 6000.2 available=true",
			"1.1.0 2021.3 available=false",
			"1.0.0 2021.3 available=true",
		}},
		// unity is the oldest Unity a version supports.
		{"unity 2022.3", VersionsOptions{Unity: "2022.3.22f1"}, []string{
			"1.1.0 2021.3 available=false",
			"1.0.0 2021.3 available=true",
		}},
	}
	for _, c := range cases {
		if got := show(c.opt); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: versions =\n%s\nwant\n%s", c.name, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
	}
	if _, err := f.app.Versions("kit", VersionsOptions{Unity: "six"}); err == nil {
		t.Error("a bad Unity version was taken")
	}
	list, err := f.app.Versions("kit/"+pkgName, VersionsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Packages) != 1 || list.Packages[0].Package != pkgName || list.Packages[0].Updated.IsZero() || list.Packages[0].Source != "npm "+registryURL {
		t.Errorf("list = %+v", list)
	}
	if _, err := f.app.Versions("kit/com.example.none", VersionsOptions{}); err == nil {
		t.Error("versions of a package the recipe does not have")
	}
}

func TestPullRefusesAVersionListOfAnotherRecipe(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	ctx := context.Background()
	if _, err := f.app.Add(ctx, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	// The user edits the recipe to read another registry.
	edited := npmRecipe("kit", "https://mirror.test", pkgName, "")
	if err := os.WriteFile(filepath.Join(f.app.Layout.Recipes(), "kit.json"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Pull(ctx, nil, PullOptions{}); err == nil || !strings.Contains(err.Error(), "vpmify recipe update kit") {
		t.Errorf("err = %v", err)
	}
	if _, err := f.app.Versions("kit", VersionsOptions{}); err == nil {
		t.Error("versions read a stale version list")
	}
	if _, err := f.app.Update(ctx, nil); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.Pull(ctx, nil, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
}

// When upstream has only prereleases, a pull that names no package warns,
// one that names the package fails, and a version named outright is
// placed.
func TestPullNamedVersionWithAnEmptyRange(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0-beta.1", "2021.3")
	ctx := context.Background()
	if _, err := f.app.Add(ctx, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	// A pull that names no package warns; one that names it fails. Both
	// point at --prerelease.
	res, err := f.app.Pull(ctx, nil, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Warning, "--prerelease") || len(res.UpToDate) != 0 {
		t.Errorf("unnamed: warnings = %v, up to date = %v", res.Warnings, res.UpToDate)
	}
	if _, err := f.app.Pull(ctx, []string{"kit/" + pkgName}, PullOptions{}); err == nil || !strings.Contains(err.Error(), "--prerelease") {
		t.Errorf("named: err = %v", err)
	}
	if _, err := f.app.Pull(ctx, []string{"kit@1.0.0-beta.1"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.0.0-beta.1@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
	// Once a version is available, the pull still warns rather than fails.
	if res, err := f.app.Pull(ctx, nil, PullOptions{}); err != nil || len(res.Warnings) != 1 {
		t.Errorf("again: err = %v, warnings = %v", err, res.Warnings)
	}
}

// A package without any version to choose is warned of without pointing
// at --prerelease, which would not help.
func TestPullOfAPackageWithoutVersions(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	ctx := context.Background()
	if _, err := f.app.Add(ctx, f.writeRecipe(`, "versions": ">=2.0.0"`)); err != nil {
		t.Fatal(err, f.log)
	}
	res, err := f.app.Pull(ctx, nil, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Warning, "no version that can be converted") || strings.Contains(res.Warnings[0].Warning, "--prerelease") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	if _, err := f.app.Pull(ctx, nil, PullOptions{Prerelease: true}); err != nil {
		t.Fatal(err, f.log)
	}
}

// Prerelease lets a pull that names no version choose prereleases, which it
// otherwise leaves for the user to name.
func TestPullPrerelease(t *testing.T) {
	cases := []struct {
		name string
		opt  PullOptions
		want []string
	}{
		{"stable", PullOptions{}, []string{"1.0.0@2021.3"}},
		{"prerelease", PullOptions{Prerelease: true}, []string{"1.1.0-beta.1@2021.3"}},
		{"all stable", PullOptions{All: true}, []string{"1.0.0@2021.3"}},
		{"all", PullOptions{All: true, Prerelease: true}, []string{"0.9.0-rc.1@2021.3", "1.0.0@2021.3", "1.1.0-beta.1@2021.3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.reg.add("0.9.0-rc.1", "2021.3")
			f.reg.add("1.0.0", "2021.3")
			f.reg.add("1.1.0-beta.1", "2021.3")
			if err := f.addAndPull(c.opt, f.writeRecipe("")); err != nil {
				t.Fatal(err, f.log)
			}
			if got := f.available(); !reflect.DeepEqual(got, c.want) {
				t.Errorf("placed = %v, want %v", got, c.want)
			}
		})
	}
}

func TestBrokenVersionListPointsAtUpdate(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if err := os.WriteFile(f.app.Layout.Index("kit"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Pull(ctx, nil, PullOptions{}); err == nil || !strings.Contains(err.Error(), "run `vpmify recipe update kit`") {
		t.Errorf("pull: err = %v", err)
	}
	problems, err := f.app.Doctor(false)
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0].Message, "run `vpmify recipe update kit`") {
		t.Errorf("doctor: problems = %v, err = %v", problemMessages(problems), err)
	}
	// update mends it.
	if _, err := f.app.Update(ctx, nil); err != nil {
		t.Fatal(err, f.log)
	}
	if problems, err := f.app.Doctor(false); err != nil || len(problems) != 0 {
		t.Errorf("after update: problems = %v, err = %v", problemMessages(problems), err)
	}
}

func TestChangedOverridesRebuildInPlace(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	before := f.registered()

	recipe := f.writeRecipe(`,
  "overrides": [{"set": {"unity": "2022.3"}}]`)
	if err := f.addAndPull(PullOptions{}, recipe); err != nil {
		t.Fatal(err, f.log)
	}
	if m := f.manifest("1.0.0"); m["unity"] != "2022.3" {
		t.Errorf("manifest not rebuilt: %v", m)
	}
	if got := f.registered(); !reflect.DeepEqual(got, before) {
		t.Errorf("registered changed: %v -> %v", before, got)
	}
	if f.reg.downloads["1.0.0"] != 1 {
		t.Errorf("downloaded %d times", f.reg.downloads["1.0.0"])
	}
}

func TestRemove(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("2.0.0", "2021.3")
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{}, f.writeRecipe(overrideNewUnity)); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.Pull(ctx, []string{"kit@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}

	if _, err := f.app.RemoveVersions("kit", "1.0.0"); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"2.0.0@6000.2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
	if _, err := os.Stat(f.app.Layout.PackageDir("kit", pkgName, "1.0.0")); !os.IsNotExist(err) {
		t.Errorf("1.0.0 still in the store: %v", err)
	}
	if _, err := f.app.RemoveVersions("kit", "1.0.0"); err == nil || !strings.Contains(err.Error(), "version 1.0.0 is not available") {
		t.Errorf("removing 1.0.0 again: err = %v", err)
	}

	// Removing every version leaves the recipe and its version list, so
	// pull takes a version again without an update.
	if _, err := f.app.RemoveVersions("kit", ""); err != nil {
		t.Fatal(err, f.log)
	}
	if got := f.available(); len(got) != 0 {
		t.Errorf("placed = %v", got)
	}
	if _, err := f.app.RemoveVersions("kit", ""); err == nil || !strings.Contains(err.Error(), "no version is available") {
		t.Errorf("removing every version again: err = %v", err)
	}
	if _, err := f.app.Pull(ctx, []string{"kit@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}

	if _, err := f.app.RemoveRecipe("kit/"+pkgName, true); err == nil {
		t.Error("RemoveRecipe took a package")
	}
	if _, err := f.app.RemoveRecipe("kit", true); err != nil {
		t.Fatal(err, f.log)
	}
	if got := f.available(); len(got) != 0 {
		t.Errorf("placed = %v", got)
	}
	if got := f.registered(); !reflect.DeepEqual(got, []string{f.foreign}) {
		t.Errorf("registered = %v", got)
	}
	for _, p := range []string{filepath.Join(f.app.Layout.Recipes(), "kit.json"), f.app.Layout.Index("kit")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there: %v", p, err)
		}
	}
}

// The display name comes from the converted package.json, with the
// recipe's overrides, wherever available versions are shown.
func TestDisplayName(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	extra := `, "overrides": [{"versions": ">=1.1.0", "set": {"displayName": "Kit"}}]`
	if err := f.addAndPull(PullOptions{All: true}, f.writeRecipe(extra)); err != nil {
		t.Fatal(err, f.log)
	}
	res, err := f.app.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range res.Entries {
		names = append(names, e.Version+"="+e.DisplayName)
	}
	if want := []string{"1.0.0=", "1.1.0=Kit"}; !reflect.DeepEqual(names, want) {
		t.Errorf("list = %v, want %v", names, want)
	}
	list, err := f.app.Versions("kit", VersionsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := list.Packages[0].DisplayName; got != "Kit" {
		t.Errorf("versions: display name = %q", got)
	}
	removed, err := f.app.RemoveVersions("kit", "1.1.0")
	if err != nil {
		t.Fatal(err, f.log)
	}
	if got := removed.Removed[0].DisplayName; got != "Kit" {
		t.Errorf("remove: display name = %q", got)
	}
}

func TestListOfRecipes(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	res, err := f.app.List("kit", "kit")
	if err != nil || len(res.Entries) != 1 {
		t.Errorf("list kit kit = %+v, %v", res, err)
	}
	// state.json is there, but "../state" is not a recipe id.
	for _, id := range []string{"nope", "../state"} {
		if _, err := f.app.List(id); err == nil || !strings.Contains(err.Error(), "no recipe") {
			t.Errorf("list %s: err = %v", id, err)
		}
	}
}

func TestRegistersEvenWhenARecipeFails(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	// Someone (or ALCOM's own "remove") drops the registration.
	s, err := alcom.Load(f.settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserPackageFolders([]string{f.foreign}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	f.reg.down = true
	if _, err := f.app.Update(ctx, nil); err == nil {
		t.Error("no error while the registry is down")
	}
	// Another recipe that cannot be read.
	if err := os.WriteFile(filepath.Join(f.app.Layout.Recipes(), "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := f.app.Pull(ctx, nil, PullOptions{}); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("err = %v", err)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v (registration not restored)", got, want)
	}
}

func TestBrokenDownloadIsRejected(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	// Serve a different archive than the one the integrity describes.
	f.reg.handler = func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, ".tgz") {
			_, _ = w.Write(testutil.TarGz(map[string]string{"package/package.json": "{}"}))
			return
		}
		f.reg.serve(w, req)
	}
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Errorf("err = %v", err)
	}
	if got := f.available(); len(got) != 0 {
		t.Errorf("placed = %v", got)
	}
}

func TestAddDraftsSavesTheRecipeAsRead(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("2.0.0", "2021.3")
	ctx := context.Background()
	p := f.writeRecipe(`, "versions": ">=1.0.0"`)

	drafts, err := f.app.ReadRecipes(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	want := Draft{ID: "kit", Packages: []DraftPackage{{Name: pkgName, Source: "npm " + registryURL, Versions: ">=1.0.0"}}, Origin: p}
	got := drafts[0]
	got.recipe, got.data = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("draft = %+v, want %+v", got, want)
	}

	// The file changes after the user saw the draft; what was shown is saved.
	if err := os.WriteFile(p, []byte(npmRecipe("kit", registryURL, pkgName, `, "versions": ">=2.0.0"`)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.AddDrafts(ctx, drafts); err != nil {
		t.Fatal(err, f.log)
	}
	list, err := f.app.Versions("kit", VersionsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Packages[0].Versions) != 2 {
		t.Errorf("versions = %+v, want 1.0.0 and 2.0.0", list.Packages[0].Versions)
	}

	again, err := f.app.ReadRecipes(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if !again[0].Replaces {
		t.Errorf("Replaces = false for a recipe the user has")
	}
}

const recipeURL = registryURL + "/recipes/kit.json"

// serveRecipe makes the fake registry also serve *content at recipeURL,
// counting the requests in *reads. A nil *content is a 404.
func (f *fixture) serveRecipe(content *string, reads *int) {
	f.reg.handler = func(w http.ResponseWriter, req *http.Request) {
		if registryURL+req.URL.Path == recipeURL {
			*reads++
			if *content == "" {
				http.NotFound(w, req)
				return
			}
			_, _ = w.Write([]byte(*content))
			return
		}
		f.reg.serve(w, req)
	}
}

// versionCount returns how many versions recipe kit's version list has.
func (f *fixture) versionCount() int {
	f.t.Helper()
	list, err := f.app.Versions("kit", VersionsOptions{All: true})
	if err != nil {
		f.t.Fatal(err)
	}
	return len(list.Packages[0].Versions)
}

func TestUpdateReadsRecipeAgainFromURL(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("2.0.0", "2021.3")
	ctx := context.Background()
	content := npmRecipe("kit", registryURL, pkgName, `, "versions": ">=2.0.0"`)
	reads := 0
	f.serveRecipe(&content, &reads)

	if _, err := f.app.Add(ctx, recipeURL); err != nil {
		t.Fatal(err, f.log)
	}
	if reads != 1 {
		t.Errorf("reads after add = %d, want 1", reads)
	}
	if got := f.versionCount(); got != 1 {
		t.Errorf("versions after add = %d, want 1", got)
	}

	content = npmRecipe("kit", registryURL, pkgName, `, "versions": ">=1.0.0"`)
	res, err := f.app.Update(ctx, nil)
	if err != nil {
		t.Fatal(err, f.log)
	}
	if reads != 2 {
		t.Errorf("reads after update = %d, want 2", reads)
	}
	if got := f.versionCount(); got != 2 {
		t.Errorf("versions after update = %d, want 2", got)
	}
	if w := res.Recipes[0].Warnings; len(w) != 0 {
		t.Errorf("warnings = %v", w)
	}
	saved, err := os.ReadFile(f.app.recipePath("kit"))
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != content {
		t.Errorf("saved recipe = %s", saved)
	}
}

func TestUpdateKeepsRecipeThatCannotBeReadAgain(t *testing.T) {
	for _, tc := range []struct {
		name, content, warning string
	}{
		{"missing", "", "404"},
		{"broken", "{", "could not read"},
		{"other id", npmRecipe("other", registryURL, pkgName, `, "versions": ">=1.0.0"`), `its id is now "other"`},
		{"newer format", `{"vpmifyFormat": 99, "id": "kit"}`, "newer Vpmify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.reg.add("1.0.0", "2021.3")
			f.reg.add("2.0.0", "2021.3")
			ctx := context.Background()
			added := npmRecipe("kit", registryURL, pkgName, `, "versions": ">=2.0.0"`)
			content := added
			reads := 0
			f.serveRecipe(&content, &reads)
			if _, err := f.app.Add(ctx, recipeURL); err != nil {
				t.Fatal(err, f.log)
			}

			content = tc.content
			res, err := f.app.Update(ctx, nil)
			if err != nil {
				t.Fatal(err, f.log)
			}
			if reads != 2 {
				t.Errorf("reads = %d, want 2", reads)
			}
			if w := res.Recipes[0].Warnings; len(w) != 1 || !strings.Contains(w[0], tc.warning) {
				t.Errorf("warnings = %v, want one with %q", w, tc.warning)
			}
			if got := f.versionCount(); got != 1 {
				t.Errorf("versions = %d, want 1 from the saved recipe", got)
			}
			saved, err := os.ReadFile(f.app.recipePath("kit"))
			if err != nil {
				t.Fatal(err)
			}
			if string(saved) != added {
				t.Errorf("saved recipe = %s", saved)
			}
		})
	}
}

func TestUpdateKeepsRereadWarningWhenSourcesFail(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	ctx := context.Background()
	content := npmRecipe("kit", registryURL, pkgName, "")
	reads := 0
	f.serveRecipe(&content, &reads)
	if _, err := f.app.Add(ctx, recipeURL); err != nil {
		t.Fatal(err, f.log)
	}

	f.reg.down = true
	res, err := f.app.Update(ctx, nil)
	if err == nil {
		t.Fatal("err = nil with the registry down")
	}
	if len(res.Recipes) != 1 || len(res.Recipes[0].Warnings) != 1 || !strings.Contains(res.Recipes[0].Warnings[0], "could not read the recipe again") {
		t.Errorf("recipes = %+v, want kit with the reread warning", res.Recipes)
	}
}

func TestUpdateDoesNotReadRecipeFromFileAgain(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("2.0.0", "2021.3")
	ctx := context.Background()
	p := f.writeRecipe(`, "versions": ">=2.0.0"`)
	if _, err := f.app.Add(ctx, p); err != nil {
		t.Fatal(err, f.log)
	}
	if err := os.WriteFile(p, []byte(npmRecipe("kit", registryURL, pkgName, `, "versions": ">=1.0.0"`)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Update(ctx, nil); err != nil {
		t.Fatal(err, f.log)
	}
	if got := f.versionCount(); got != 1 {
		t.Errorf("versions = %d, want 1 from the saved recipe", got)
	}
}

func TestAddRefusesRecipesOverPlainHTTP(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	// A file named like the URL, which must not be read in its place.
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(filepath.Join(dir, "http:", "registry.test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "http:", "registry.test", "kit.json"), []byte(npmRecipe("kit", registryURL, pkgName, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Add(context.Background(), "http://registry.test/kit.json"); err == nil || !strings.Contains(err.Error(), "only https") {
		t.Errorf("err = %v", err)
	}
}

func TestAddRefusesRecipeOverTheLimit(t *testing.T) {
	f := newFixture(t)
	content := npmRecipe("kit", registryURL, pkgName, `, "note": "`+strings.Repeat("x", maxRecipeSize)+`"`)
	reads := 0
	f.serveRecipe(&content, &reads)
	if _, err := f.app.Add(context.Background(), recipeURL); !errors.Is(err, fetch.ErrTooLarge) {
		t.Errorf("err = %v", err)
	}
}

func TestAddDraftsRefusesDraftsNotRead(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.AddDrafts(context.Background(), []Draft{{ID: "kit"}}); err == nil {
		t.Error("err = nil")
	}
}

func TestRecipesNamesProblems(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	ctx := context.Background()
	p := f.writeRecipe("")
	if _, err := f.app.Add(ctx, p); err != nil {
		t.Fatal(err, f.log)
	}
	if err := os.WriteFile(filepath.Join(f.app.Layout.Recipes(), "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	unread := npmRecipe("unread", registryURL, "com.example.other", "")
	if err := os.WriteFile(filepath.Join(f.app.Layout.Recipes(), "unread.json"), []byte(unread), 0o644); err != nil {
		t.Fatal(err)
	}

	recipes, err := f.app.Recipes()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]RecipeInfo{}
	for _, r := range recipes {
		got[r.ID] = r
	}
	if r := got["kit"]; !reflect.DeepEqual(r.Packages, []string{pkgName}) || r.Origin != p || r.Problem != "" {
		t.Errorf("kit = %+v", r)
	}
	if r := got["broken"]; r.Problem == "" {
		t.Errorf("broken = %+v, want a problem", r)
	}
	if r := got["unread"]; !reflect.DeepEqual(r.Packages, []string{"com.example.other"}) || !strings.Contains(r.Problem, "update") {
		t.Errorf("unread = %+v, want a problem naming update", r)
	}
	if len(recipes) != 3 {
		t.Errorf("recipes = %+v", recipes)
	}
}
