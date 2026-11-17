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

const registryURL = "http://registry.test"

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
func (f *fixture) writeRecipe(extra string) string {
	f.t.Helper()
	content := `{
  "id": "kit",
  "source": {"type": "npm", "registry": "` + registryURL + `", "name": "` + pkgName + `"}` + extra + `
}`
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
	if err := f.app.Add(ctx, refs...); err != nil {
		return err
	}
	return f.app.Pull(ctx, nil, opt)
}

func (f *fixture) available() []string {
	f.t.Helper()
	entries, settingsErr, err := f.app.List()
	if err != nil || settingsErr != nil {
		f.t.Fatal(err, settingsErr)
	}
	var out []string
	for _, e := range entries {
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
	data, err := os.ReadFile(filepath.Join(f.app.Layout.PackageDir(pkgName, version), "package.json"))
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

func TestPullPlacesLatestPerUnityAndRegisters(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	f.reg.add("2.0.0", "2021.3")
	f.reg.add("2.1.0-beta.1", "2021.3")
	ctx := context.Background()

	// add reads the version list and places nothing.
	if err := f.app.Add(ctx, f.writeRecipe(overrideNewUnity)); err != nil {
		t.Fatal(err, f.log)
	}
	if got := f.available(); len(got) != 0 {
		t.Errorf("placed after add = %v", got)
	}
	if len(f.reg.downloads) != 0 {
		t.Errorf("add downloaded %v", f.reg.downloads)
	}

	if err := f.app.Pull(ctx, nil, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.1.0@2021.3", "2.0.0@6000.2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
	wantFolders := []string{
		f.app.Layout.PackageDir(pkgName, "1.1.0"),
		f.app.Layout.PackageDir(pkgName, "2.0.0"),
		f.foreign,
	}
	sort.Strings(wantFolders)
	if got := f.registered(); !reflect.DeepEqual(got, wantFolders) {
		t.Errorf("registered = %v, want %v", got, wantFolders)
	}
	if m := f.manifest("2.0.0"); m["unity"] != "6000.2" || m["unityRelease"] != "6f2" {
		t.Errorf("2.0.0 manifest = %v", m)
	}
	if m := f.manifest("1.1.0"); m["unity"] != "2021.3" || m["unityRelease"] != nil {
		t.Errorf("1.1.0 manifest = %v", m)
	}
	if _, err := os.Stat(filepath.Join(f.app.Layout.PackageDir(pkgName, "2.0.0"), "Runtime", "Kit.cs")); err != nil {
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
	if err := f.app.Pull(ctx, nil, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed before update = %v, want %v", got, want)
	}
	if err := f.app.Update(ctx, nil); err != nil {
		t.Fatal(err, f.log)
	}
	if !strings.Contains(f.log.String(), "new: 1.2.0") {
		t.Errorf("update did not name the new version:\n%s", f.log)
	}
	if err := f.app.Pull(ctx, nil, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.1.0@2021.3", "1.2.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}

	if err := f.app.Pull(ctx, []string{"kit@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3", "1.1.0@2021.3", "1.2.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}

	if err := f.app.Pull(ctx, []string{"kit@9.9.9"}, PullOptions{}); err == nil {
		t.Error("no error for a version the version list does not have")
	}

	// Nothing is downloaded twice.
	for v, n := range f.reg.downloads {
		if n != 1 {
			t.Errorf("%s downloaded %d times", v, n)
		}
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
	if err := f.app.Add(ctx, f.writeRecipe(`, "versions": ">=1.0.0"`+overrideNewUnity)); err != nil {
		t.Fatal(err, f.log)
	}
	if err := f.app.Pull(ctx, []string{"kit@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if err := f.app.Remove(ctx, "kit", "1.1.0"); err != nil {
		t.Fatal(err, f.log)
	}
	// The version list keeps only the recipe's range, so 0.9.0 is not taken.
	if err := f.app.Pull(ctx, []string{"kit@0.9.0"}, PullOptions{}); err == nil || !strings.Contains(err.Error(), "outside the recipe's versions") {
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
		for _, v := range list.Versions {
			out = append(out, fmt.Sprintf("%s %s newest=%v available=%v", v.Version, v.Unity, v.Newest, v.Available))
		}
		return out
	}
	cases := []struct {
		name string
		opt  VersionsOptions
		want []string
	}{
		{"stable", VersionsOptions{}, []string{
			"2.0.0 6000.2 newest=true available=true",
			"1.1.0 2021.3 newest=true available=false",
			"1.0.0 2021.3 newest=false available=true",
		}},
		{"all", VersionsOptions{All: true}, []string{
			"2.1.0-beta.1 6000.2 newest=false available=false",
			"2.0.0 6000.2 newest=true available=true",
			"1.1.0 2021.3 newest=true available=false",
			"1.0.0 2021.3 newest=false available=true",
		}},
		{"available", VersionsOptions{Available: true}, []string{
			"2.0.0 6000.2 newest=true available=true",
			"1.0.0 2021.3 newest=false available=true",
		}},
		// unity is the oldest Unity a version supports.
		{"unity 2022.3", VersionsOptions{Unity: "2022.3.22f1"}, []string{
			"1.1.0 2021.3 newest=true available=false",
			"1.0.0 2021.3 newest=false available=true",
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
	list, err := f.app.Versions("kit", VersionsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Package != pkgName || list.Updated.IsZero() {
		t.Errorf("list = %+v", list)
	}
}

func TestPullRefusesAVersionListOfAnotherRecipe(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	ctx := context.Background()
	if err := f.app.Add(ctx, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	// The user edits the recipe to read another registry.
	edited := `{"id": "kit", "source": {"type": "npm", "registry": "http://mirror.test", "name": "` + pkgName + `"}}`
	if err := os.WriteFile(filepath.Join(f.app.Layout.Recipes(), "kit.json"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Pull(ctx, nil, PullOptions{}); err == nil || !strings.Contains(err.Error(), "vpmify update kit") {
		t.Errorf("err = %v", err)
	}
	if _, err := f.app.Versions("kit", VersionsOptions{}); err == nil {
		t.Error("versions read a stale version list")
	}
	if err := f.app.Update(ctx, nil); err != nil {
		t.Fatal(err, f.log)
	}
	if err := f.app.Pull(ctx, nil, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
}

// A version named outright is placed even when the recipe's range is
// empty, as when upstream has only prereleases.
func TestPullNamedVersionWithAnEmptyRange(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0-beta.1", "2021.3")
	ctx := context.Background()
	if err := f.app.Add(ctx, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if err := f.app.Pull(ctx, nil, PullOptions{}); err == nil || !strings.Contains(err.Error(), "no version in the version list matches") {
		t.Errorf("err = %v", err)
	}
	if err := f.app.Pull(ctx, []string{"kit@1.0.0-beta.1"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.0.0-beta.1@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
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
	if err := f.app.Pull(ctx, nil, PullOptions{}); err == nil || !strings.Contains(err.Error(), "run `vpmify update kit`") {
		t.Errorf("pull: err = %v", err)
	}
	problems, err := f.app.Doctor(false)
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0].Message, "run `vpmify update kit`") {
		t.Errorf("doctor: problems = %v, err = %v", problemMessages(problems), err)
	}
	// update mends it.
	if err := f.app.Update(ctx, nil); err != nil {
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

	if err := f.app.Remove(ctx, "kit", "1.0.0"); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"2.0.0@6000.2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
	if _, err := os.Stat(f.app.Layout.PackageDir(pkgName, "1.0.0")); !os.IsNotExist(err) {
		t.Errorf("1.0.0 still in the store: %v", err)
	}

	if err := f.app.Remove(ctx, "kit", ""); err != nil {
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
	if err := f.app.Update(ctx, nil); err == nil {
		t.Error("no error while the registry is down")
	}
	// Another recipe that cannot be read.
	if err := os.WriteFile(filepath.Join(f.app.Layout.Recipes(), "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := f.app.Pull(ctx, nil, PullOptions{}); err == nil || !strings.Contains(err.Error(), "broken") {
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
