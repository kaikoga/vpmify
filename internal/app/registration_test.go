package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/kaikoga/vpmify/internal/alcom"
)

// unregister takes folders out of ALCOM's settings.json, as the user does
// in ALCOM.
func (f *fixture) unregister(dirs ...string) {
	f.t.Helper()
	s, err := alcom.Load(f.settings)
	if err != nil {
		f.t.Fatal(err)
	}
	folders, err := s.UserPackageFolders()
	if err != nil {
		f.t.Fatal(err)
	}
	folders = slices.DeleteFunc(folders, func(p string) bool { return slices.Contains(dirs, p) })
	if err := s.SetUserPackageFolders(folders); err != nil {
		f.t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		f.t.Fatal(err)
	}
}

// A version the user unregistered in ALCOM stays so until a pull takes it
// again, which registers it from the store without converting it.
func TestPullRegistersOnlyWhatItTakes(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{All: true}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	old := f.app.Layout.PackageDir("kit", pkgName, "1.0.0")
	f.unregister(old)

	res, err := f.app.Pull(ctx, []string{"kit"}, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(res.Registered) != 0 || !reflect.DeepEqual(res.UpToDate, []string{"kit"}) {
		t.Errorf("pull = %+v", res)
	}
	if got, want := f.available(), []string{"1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("available = %v, want %v", got, want)
	}

	// Removing another version does not register it either.
	removed, err := f.app.RemoveVersions("kit", "1.1.0")
	if err != nil {
		t.Fatal(err, f.log)
	}
	if want := []string{f.app.Layout.PackageDir("kit", pkgName, "1.1.0")}; !reflect.DeepEqual(removed.Unregistered, want) {
		t.Errorf("unregistered = %v, want %v", removed.Unregistered, want)
	}
	if got := f.available(); len(got) != 0 {
		t.Errorf("available = %v", got)
	}

	res, err = f.app.Pull(ctx, []string{"kit@1.0.0"}, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(res.Built) != 0 || !reflect.DeepEqual(res.Registered, []string{old}) || len(res.UpToDate) != 0 {
		t.Errorf("pull = %+v", res)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("available = %v, want %v", got, want)
	}
	if f.reg.downloads["1.0.0"] != 1 {
		t.Errorf("1.0.0 downloaded %d times", f.reg.downloads["1.0.0"])
	}
}

// What versions lists as available follows ALCOM, and the package's name
// comes from the store whether a version is available or not. Without
// settings.json nothing can be told available.
func TestVersionsAvailableFollowsALCOM(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{All: true}, f.writeRecipe(`, "overrides": [{"set": {"displayName": "Kit"}}]`)); err != nil {
		t.Fatal(err, f.log)
	}
	f.unregister(f.app.Layout.PackageDir("kit", pkgName, "1.1.0"), f.app.Layout.PackageDir("kit", pkgName, "1.0.0"))
	if _, err := f.app.Pull(ctx, []string{"kit@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	list, err := f.app.Versions("kit", VersionsOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	pv := list.Packages[0]
	var got []string
	for _, v := range pv.Versions {
		got = append(got, v.Version+" "+map[bool]string{true: "available", false: "-"}[v.Available])
	}
	if want := []string{"1.1.0 -", "1.0.0 available"}; !reflect.DeepEqual(got, want) || pv.DisplayName != "Kit" {
		t.Errorf("versions = %v (%q), want %v (Kit)", got, pv.DisplayName, want)
	}

	if err := os.Remove(f.settings); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Versions("kit", VersionsOptions{}); err == nil || !strings.Contains(err.Error(), "settings.json not found") {
		t.Errorf("versions without settings.json: err = %v", err)
	}
	if _, err := f.app.List(); err == nil || !strings.Contains(err.Error(), "settings.json not found") {
		t.Errorf("list without settings.json: err = %v", err)
	}
}

// A changed recipe builds again the versions that are available when it is
// pulled, and a version in the store that is not, only when it is pulled.
func TestPullBuildsAgainOnlyAvailableVersions(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{All: true}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	old := f.app.Layout.PackageDir("kit", pkgName, "1.0.0")
	f.unregister(old)
	f.writeStoredRecipe("kit", `, "overrides": [{"set": {"displayName": "X"}}]`)
	f.log.Reset()

	if _, err := f.app.Pull(ctx, []string{"kit"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if m := f.manifest("1.1.0"); m["displayName"] != "X" {
		t.Errorf("1.1.0 not built again: %v", m)
	}
	if m := f.manifest("1.0.0"); m["displayName"] != nil {
		t.Errorf("1.0.0 built again while not available: %v", m)
	}

	if _, err := f.app.Pull(ctx, []string{"kit@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if m := f.manifest("1.0.0"); m["displayName"] != "X" {
		t.Errorf("1.0.0 not built again when pulled: %v", m)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3", "1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("available = %v, want %v", got, want)
	}
}

// An id no recipe can have fails that target alone, and the other recipes
// are pulled.
func TestPullFailsOnlyTheIDsNoRecipeHas(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	ctx := context.Background()
	if _, err := f.app.Add(ctx, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	res, err := f.app.Pull(ctx, []string{"KIT", "kit", "..", "."}, PullOptions{})
	if err == nil {
		t.Error("no error for ids no recipe has")
	}
	var failed []string
	for _, e := range res.Errors {
		failed = append(failed, e.Recipe)
	}
	if want := []string{"KIT", "..", "."}; !reflect.DeepEqual(failed, want) {
		t.Errorf("errors for %v, want %v", failed, want)
	}
	if len(res.Built) != 1 || res.Built[0].Recipe != "kit" {
		t.Errorf("built = %+v", res.Built)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("available = %v, want %v", got, want)
	}
}

// readOnly makes dir unwritable until undo is called or the test ends, so
// that saving a file in it fails.
func readOnly(t *testing.T, dir string) (undo func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("cannot make a folder unwritable on Windows")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	undo = func() { _ = os.Chmod(dir, 0o755) }
	t.Cleanup(undo)
	return undo
}

// A remove that cannot change settings.json changes nothing, so that the
// same remove finishes it later.
func TestRemoveThatCannotUnregisterCanRunAgain(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{All: true}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	undo := readOnly(t, filepath.Dir(f.settings))
	if res, err := f.app.RemoveVersions("kit", "1.0.0"); err == nil || res != nil {
		t.Errorf("remove = %+v, %v; want nil and an error", res, err)
	}
	undo()
	if _, err := f.app.RemoveVersions("kit", "1.0.0"); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("available = %v, want %v", got, want)
	}
	res, err := f.app.Pull(ctx, []string{"kit"}, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(res.Built) != 0 {
		t.Errorf("pull built %+v", res.Built)
	}
}

// doctor --fix that cannot change settings.json keeps the records, so that
// the next doctor finds the problems again.
func TestDoctorFixThatCannotUnregisterFindsTheProblemAgain(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	if err := f.addAndPull(PullOptions{All: true}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if err := os.RemoveAll(f.app.Layout.PackageDir("kit", pkgName, "1.0.0")); err != nil {
		t.Fatal(err)
	}
	undo := readOnly(t, filepath.Dir(f.settings))
	if _, err := f.app.Doctor(true); err == nil {
		t.Error("no error when settings.json cannot be written")
	}
	undo()
	problems, err := f.app.Doctor(false)
	if err != nil || len(problems) != 1 || !strings.Contains(problems[0].Message, "1.0.0: folder") {
		t.Errorf("problems = %v, err = %v", problemMessages(problems), err)
	}
	if _, err := f.app.Doctor(true); err != nil {
		t.Fatal(err, f.log)
	}
	want := []string{f.foreign, f.app.Layout.PackageDir("kit", pkgName, "1.1.0")}
	slices.Sort(want)
	if got := f.registered(); !reflect.DeepEqual(got, want) {
		t.Errorf("registered = %v, want %v", got, want)
	}
}

// A version pull registered without recording it, as when saving
// state.json failed, is listed and can be removed.
func TestListedVersionsCanBeRemoved(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	ctx := context.Background()
	if _, err := f.app.Add(ctx, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.Pull(ctx, []string{"kit@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	undo := readOnly(t, f.app.Layout.Home())
	if _, err := f.app.Pull(ctx, []string{"kit@1.1.0"}, PullOptions{}); err == nil {
		t.Error("no error when state.json cannot be written")
	}
	undo()
	if got, want := f.available(), []string{"1.0.0@2021.3", "1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("available = %v, want %v", got, want)
	}
	removed, err := f.app.RemoveVersions("kit", "1.1.0")
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(removed.Removed) != 1 || len(removed.Unregistered) != 1 {
		t.Errorf("remove = %+v", removed)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("available = %v, want %v", got, want)
	}
	if problems, err := f.app.Doctor(false); err != nil || len(problems) != 0 {
		t.Errorf("problems = %v, err = %v", problemMessages(problems), err)
	}
}

// A pull looks at the dependencies of the versions it takes only, and a
// dependency already in the store is registered without converting it.
func TestPullAddsTheDependenciesOfWhatItTakes(t *testing.T) {
	f := newFixture(t)
	f.app.Client.HTTP.Transport = monorepo("1.0.0", "1.1.0")
	ctx := context.Background()
	if _, err := f.app.Add(ctx,
		f.writeGitRecipe("pkg", "com.example.pkg", "Packages/Pkg", pkgNeedsBase),
		f.writeGitRecipe("base", "com.example.base", "Packages/Base", "")); err != nil {
		t.Fatal(err, f.log)
	}
	base := func(v string) string { return f.app.Layout.PackageDir("base", "com.example.base", v) }
	pkg := func(v string) string { return f.app.Layout.PackageDir("pkg", "com.example.pkg", v) }
	if _, err := f.app.Pull(ctx, []string{"pkg@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}

	// pkg 1.0.0 is not pulled, so base 1.0.0 that it needed is not
	// registered again.
	f.unregister(base("1.0.0"))
	if _, err := f.app.Pull(ctx, []string{"pkg"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	want := []string{base("1.1.0"), f.foreign, pkg("1.0.0"), pkg("1.1.0")}
	slices.Sort(want)
	if got := f.registered(); !reflect.DeepEqual(got, want) {
		t.Errorf("registered = %v, want %v", got, want)
	}

	// Pulled again, pkg 1.0.0 gets the base it names from the store.
	f.unregister(base("1.1.0"))
	res, err := f.app.Pull(ctx, []string{"pkg@1.0.0"}, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(res.Built) != 0 || !reflect.DeepEqual(res.Registered, []string{base("1.0.0")}) {
		t.Errorf("pull = %+v", res)
	}

	// A dependency ALCOM lists is left as it is.
	res, err = f.app.Pull(ctx, []string{"pkg@1.0.0"}, PullOptions{})
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(res.Built) != 0 || len(res.Registered) != 0 || !reflect.DeepEqual(res.UpToDate, []string{"pkg"}) {
		t.Errorf("pull = %+v", res)
	}
	if n := strings.Count(f.log.String(), "converting com.example.base 1.0.0"); n != 1 {
		t.Errorf("base 1.0.0 converted %d times", n)
	}
}
