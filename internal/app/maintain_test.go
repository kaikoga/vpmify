package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/kaikoga/vpmify/internal/alcom"
	"github.com/kaikoga/vpmify/internal/state"
)

func problemMessages(problems []Problem) []string {
	var out []string
	for _, p := range problems {
		out = append(out, p.Message)
	}
	return out
}

// noListRecipe is the recipe of the first *NoListError in err, or "".
func noListRecipe(err error) string {
	var e *NoListError
	if errors.As(err, &e) {
		return e.Recipe
	}
	return ""
}

func TestDoctorFindsNothingAfterPull(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	problems, err := f.app.Doctor(false)
	if err != nil || len(problems) != 0 {
		t.Errorf("problems = %v, err = %v", problemMessages(problems), err)
	}
}

func TestDoctorFixes(t *testing.T) {
	f := newFixture(t)
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		f.reg.add(v, "2021.3")
	}
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{All: true}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	layout := f.app.Layout
	// 1.0.0's folder is gone, 1.1.0's package.json says another version,
	// a stray folder sits in the store, and a build was left unfinished.
	// ALCOM lost 2.0.0 and lists the stray folder, which doctor does not
	// report: what ALCOM lists is the user's.
	if err := os.RemoveAll(layout.PackageDir("kit", pkgName, "1.0.0")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.PackageDir("kit", pkgName, "1.1.0"), "package.json"), []byte(`{"name": "`+pkgName+`", "version": "9.9.9"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stray := layout.PackageDir("kit", "com.example.stray", "0.1.0")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(layout.Tmp(), "build-123")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := alcom.Load(f.settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserPackageFolders([]string{f.foreign, layout.PackageDir("kit", pkgName, "1.0.0"), layout.PackageDir("kit", pkgName, "1.1.0"), stray}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	problems, err := f.app.Doctor(false)
	if err != nil {
		t.Fatal(err, f.log)
	}
	wantSubstrings := []string{
		"1.0.0: folder",
		"1.1.0: package.json version",
		"com.example.stray is in the store",
		"build-123 is left",
	}
	msgs := problemMessages(problems)
	for _, w := range wantSubstrings {
		found := false
		for _, m := range msgs {
			found = found || strings.Contains(m, w)
		}
		if !found {
			t.Errorf("no problem with %q in %v", w, msgs)
		}
	}
	if len(msgs) != len(wantSubstrings) {
		t.Errorf("problems = %v", msgs)
	}
	for _, p := range problems {
		if p.Fix == "" || p.Fixed {
			t.Errorf("problem %+v", p)
		}
	}

	problems, err = f.app.Doctor(true)
	if err != nil {
		t.Fatal(err, f.log)
	}
	for _, p := range problems {
		if !p.Fixed {
			t.Errorf("not fixed: %+v", p)
		}
	}
	// The folders --fix forgets or deletes are unregistered, the stray one
	// with the package folder it is in; 2.0.0 stays unregistered.
	if got := f.available(); len(got) != 0 {
		t.Errorf("available = %v", got)
	}
	if got := f.registered(); !reflect.DeepEqual(got, []string{f.foreign}) {
		t.Errorf("registered = %v, want %v", got, []string{f.foreign})
	}
	for _, gone := range []string{layout.PackageDir("kit", pkgName, "1.1.0"), filepath.Dir(stray), leftover} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", gone, err)
		}
	}
	if problems, err := f.app.Doctor(false); err != nil || len(problems) != 0 {
		t.Errorf("after fix: problems = %v, err = %v", problemMessages(problems), err)
	}

	// pull --all places the forgotten versions again, and registers 2.0.0
	// from the store.
	if _, err := f.app.Pull(ctx, nil, PullOptions{All: true}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3", "1.1.0@2021.3", "2.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed after pull = %v, want %v", got, want)
	}
	if f.reg.downloads["2.0.0"] != 1 {
		t.Errorf("2.0.0 downloaded %d times", f.reg.downloads["2.0.0"])
	}
}

// A gone recipe whose versions were all removed is reported as a record
// left behind, not as one with available versions.
func TestDoctorTellsAGoneRecipeWithoutVersions(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if err := os.Remove(f.app.recipePath("kit")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.RemoveVersions("kit", ""); err != nil {
		t.Fatal(err, f.log)
	}
	problems, err := f.app.Doctor(false)
	if err != nil {
		t.Fatal(err, f.log)
	}
	msgs := problemMessages(problems)
	if len(msgs) != 1 || strings.Contains(msgs[0], "versions are in the store") || !strings.Contains(msgs[0], "`vpmify recipe remove kit` forgets it") {
		t.Errorf("problems = %v", msgs)
	}
	if _, err := f.app.RemoveRecipe("kit", true); err != nil {
		t.Fatal(err, f.log)
	}
	if problems, err := f.app.Doctor(false); err != nil || len(problems) != 0 {
		t.Errorf("after recipe remove: problems = %v, err = %v", problemMessages(problems), err)
	}
}

func TestDoctorReportsWhatItCannotFix(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if err := os.Remove(f.app.recipePath("kit")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.app.recipePath("broken"), []byte(`{"vpmifyFormat": 1, "displayName": "", "id": "broken"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, err := f.app.Doctor(true)
	if err != nil {
		t.Fatal(err, f.log)
	}
	msgs := problemMessages(problems)
	if len(problems) != 2 || !strings.Contains(msgs[0], "broken: recipe cannot be read") || !strings.Contains(msgs[1], "vpmify recipe remove kit") {
		t.Errorf("problems = %v", msgs)
	}
	for _, p := range problems {
		if p.Fixed {
			t.Errorf("fixed %+v", p)
		}
	}
	// The placed version stays.
	if got, want := f.available(), []string{"1.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
}

func (f *fixture) writeStoredRecipe(id, extra string) {
	f.t.Helper()
	content := npmRecipe(id, registryURL, pkgName, extra)
	if err := os.WriteFile(f.app.recipePath(id), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestRemoveDeletesTheEmptyPackageFolder(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.RemoveVersions("kit", "1.0.0"); err != nil {
		t.Fatal(err, f.log)
	}
	for _, p := range []string{filepath.Join(f.app.Layout.Packages(), "kit", pkgName), filepath.Join(f.app.Layout.Packages(), "kit")} {
		if exists(p) {
			t.Errorf("%s is left", p)
		}
	}
	if problems, err := f.app.Doctor(false); err != nil || len(problems) != 0 {
		t.Errorf("problems = %v, err = %v", problemMessages(problems), err)
	}
}

func TestRemoveWithoutSettingsChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if err := os.Remove(f.settings); err != nil {
		t.Fatal(err)
	}
	if res, err := f.app.RemoveVersions("kit", "1.0.0"); err == nil || res != nil {
		t.Errorf("remove without settings.json = %+v, %v; want nil and an error", res, err)
	}
	st, err := state.Load(f.app.Layout.State())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Recipes["kit"].Packages[pkgName].Versions) != 1 {
		t.Errorf("state changed: %+v", st.Recipes["kit"])
	}
	if !exists(f.app.Layout.PackageDir("kit", pkgName, "1.0.0")) {
		t.Error("the folder was deleted")
	}
}

// lockFolder makes the files in dir undeletable until the test ends.
func lockFolder(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// The read-only attribute on a folder does not keep its files.
		t.Skip("cannot lock a folder on Windows")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// A remove that fails after forgetting the version still reports what it
// changed: the version is gone and ALCOM no longer has it.
func TestRemoveReportsWhatItDidBeforeFailing(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	dir := f.app.Layout.PackageDir("kit", pkgName, "1.0.0")
	lockFolder(t, dir)
	res, err := f.app.RemoveVersions("kit", "1.0.0")
	if err == nil {
		t.Fatal("no error for a folder that cannot be deleted")
	}
	if res == nil || len(res.Removed) != 1 || !reflect.DeepEqual(res.Unregistered, []string{dir}) {
		t.Errorf("remove = %+v", res)
	}
	if got := f.available(); len(got) != 0 {
		t.Errorf("available = %v", got)
	}
}

// An id no recipe can have names no folder: "." would be the store, ".."
// Vpmify's folder, and "KIT" kit's folders where the file system ignores
// case.
func TestRemoveRefusesIDsNoRecipeHas(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	before := f.registered()
	for _, id := range []string{".", "..", "KIT"} {
		if res, err := f.app.RemoveRecipe(id, false); err == nil || res != nil {
			t.Errorf("RemoveRecipe(%q, false) = %+v, %v; want nil and an error", id, res, err)
		}
		if res, err := f.app.RemoveRecipe(id, true); err == nil || res != nil {
			t.Errorf("RemoveRecipe(%q, true) = %+v, %v; want nil and an error", id, res, err)
		}
		if res, err := f.app.RemoveVersions(id, ""); err == nil || res != nil {
			t.Errorf("RemoveVersions(%q) = %+v, %v; want nil and an error", id, res, err)
		}
	}
	if got := f.registered(); !reflect.DeepEqual(got, before) {
		t.Errorf("registered = %v, want %v", got, before)
	}
	for _, p := range []string{f.app.Layout.PackageDir("kit", pkgName, "1.0.0"), f.app.Layout.Cache(), f.app.recipePath("kit")} {
		if !exists(p) {
			t.Errorf("%s was deleted", p)
		}
	}
}

// A record whose keys name a folder other than its own version's, as only
// a state.json edited by hand has, deletes nothing; doctor forgets it.
func TestARecordThatNamesAnotherFolderDeletesNothing(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	f.writeStoredRecipe("kit2", "")
	st, err := state.Load(f.app.Layout.State())
	if err != nil {
		t.Fatal(err)
	}
	// <store>/kit2/../kit is kit's folder.
	st.Recipe("kit2").Package("..").Versions["kit"] = &state.VersionState{Fingerprint: "x"}
	if err := st.Save(f.app.Layout.State()); err != nil {
		t.Fatal(err)
	}
	before := f.registered()

	problems, err := f.app.Doctor(false)
	if err != nil {
		t.Fatal(err, f.log)
	}
	found := false
	for _, m := range problemMessages(problems) {
		found = found || strings.Contains(m, "names no version folder in the store")
	}
	if !found {
		t.Errorf("problems = %v", problemMessages(problems))
	}
	if _, err := f.app.RemoveVersions("kit2", ""); err == nil {
		t.Error("RemoveVersions took a record that names no version folder")
	}
	if _, err := f.app.Doctor(true); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.RemoveRecipe("kit2", true); err != nil {
		t.Fatal(err, f.log)
	}
	if !exists(f.app.Layout.PackageDir("kit", pkgName, "1.0.0")) {
		t.Error("kit's version was deleted")
	}
	if got := f.registered(); !reflect.DeepEqual(got, before) {
		t.Errorf("registered = %v, want %v", got, before)
	}
	if problems, err := f.app.Doctor(false); err != nil || len(problems) != 0 {
		t.Errorf("problems = %v, err = %v", problemMessages(problems), err)
	}
}

func TestRemoveOfNoRecipeFails(t *testing.T) {
	f := newFixture(t)
	res, err := f.app.RemoveRecipe("nosuch", true)
	if err == nil || !strings.Contains(err.Error(), `no recipe "nosuch"`) || res != nil {
		t.Errorf("remove nosuch = %+v, %v; want nil and no recipe", res, err)
	}
}

// A clean that fails after unregistering says it changed things.
func TestCleanReportsChangesBeforeFailing(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	lockFolder(t, f.app.Layout.PackageDir("kit", pkgName, "1.0.0"))
	_, changed, err := f.app.Clean(true)
	if err == nil || !changed {
		t.Errorf("clean = %v, %v; want changed and an error", changed, err)
	}
	if got := f.registered(); !reflect.DeepEqual(got, []string{f.foreign}) {
		t.Errorf("registered = %v", got)
	}
}

func TestCleanDeletesPlacedVersionsAndKeepsRecipes(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.Pull(ctx, []string{"kit@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}

	entries, changed, err := f.app.Clean(false)
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(entries) != 2 || entries[0].Version != "1.0.0" || entries[1].Version != "1.1.0" || changed {
		t.Errorf("entries = %+v, changed = %v", entries, changed)
	}
	if got := f.available(); len(got) != 2 {
		t.Errorf("placed without apply = %v", got)
	}

	if _, changed, err := f.app.Clean(true); err != nil || !changed {
		t.Fatal(err, changed, f.log)
	}
	if got := f.available(); len(got) != 0 {
		t.Errorf("placed = %v", got)
	}
	if got := f.registered(); !reflect.DeepEqual(got, []string{f.foreign}) {
		t.Errorf("registered = %v", got)
	}
	for _, d := range []string{f.app.Layout.Packages(), f.app.Layout.Cache(), f.app.Layout.Tmp()} {
		if exists(d) {
			t.Errorf("%s is left", d)
		}
	}
	if !exists(f.app.recipePath("kit")) {
		t.Error("the recipe was deleted")
	}

	if !exists(f.app.Layout.Index("kit")) {
		t.Error("the version list was deleted")
	}

	// pull places the newest version again, from a new download.
	if _, err := f.app.Pull(ctx, nil, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed after pull = %v, want %v", got, want)
	}
	if f.reg.downloads["1.1.0"] != 2 {
		t.Errorf("1.1.0 downloaded %d times", f.reg.downloads["1.1.0"])
	}
}

func TestCleanWithoutSettings(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if err := os.Remove(f.settings); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.app.Clean(true); err != nil {
		t.Fatal(err, f.log)
	}
	if exists(f.app.Layout.Packages()) {
		t.Error("the store is left")
	}
}

func TestRefetchBuildsEveryPlacedVersionAgain(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.Pull(ctx, []string{"kit@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	before := f.registered()
	broken := filepath.Join(f.app.Layout.PackageDir("kit", pkgName, "1.0.0"), "Runtime", "Kit.cs")
	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}

	if _, err := f.app.Pull(ctx, nil, PullOptions{Refetch: true}); err != nil {
		t.Fatal(err, f.log)
	}
	if !exists(broken) {
		t.Errorf("%s was not built again", broken)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3", "1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
	if got := f.registered(); !reflect.DeepEqual(got, before) {
		t.Errorf("registered changed: %v -> %v", before, got)
	}
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if f.reg.downloads[v] != 2 {
			t.Errorf("%s downloaded %d times", v, f.reg.downloads[v])
		}
	}
}

func TestRefetchLeavesVersionsUpstreamNoLongerHas(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	ctx := context.Background()
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.Pull(ctx, []string{"kit@1.0.0"}, PullOptions{}); err != nil {
		t.Fatal(err, f.log)
	}
	f.reg.mu.Lock()
	delete(f.reg.versions, "1.0.0")
	delete(f.reg.tarballs, "1.0.0")
	f.reg.mu.Unlock()
	if _, err := f.app.Update(ctx, nil); err != nil {
		t.Fatal(err, f.log)
	}

	if _, err := f.app.Pull(ctx, []string{"kit"}, PullOptions{Refetch: true}); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.available(), []string{"1.0.0@2021.3", "1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
	if !strings.Contains(f.log.String(), "the version list no longer has "+pkgName+" 1.0.0") {
		t.Errorf("log = %s", f.log)
	}
}

func TestDoctorFixWithoutSettingsChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	dir := f.app.Layout.PackageDir("kit", pkgName, "1.0.0")
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.settings); err != nil {
		t.Fatal(err)
	}
	problems, err := f.app.Doctor(true)
	if err == nil {
		t.Error("no error without settings.json")
	}
	for _, p := range problems {
		if p.Fixed {
			t.Errorf("fixed %+v", p)
		}
	}
	if !exists(dir) {
		t.Errorf("%s was deleted", dir)
	}
	st, err := state.Load(f.app.Layout.State())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Recipes["kit"].Packages[pkgName].Versions) != 1 {
		t.Errorf("state changed: %+v", st.Recipes["kit"])
	}
}

func TestDoctorIgnoresFileBrowserClutter(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	for _, p := range []string{
		filepath.Join(f.app.Layout.Packages(), ".DS_Store"),
		filepath.Join(f.app.Layout.Packages(), "kit", ".DS_Store"),
		filepath.Join(f.app.Layout.Packages(), "kit", pkgName, ".DS_Store"),
	} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if problems, err := f.app.Doctor(false); err != nil || len(problems) != 0 {
		t.Errorf("problems = %v, err = %v", problemMessages(problems), err)
	}
}

// A second recipe for one package has its own folders, and ALCOM gets both.
func TestASecondRecipeForOnePackageHasItsOwnFolders(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	other := filepath.Join(t.TempDir(), "kit2.json")
	if err := os.WriteFile(other, []byte(npmRecipe("kit2", registryURL, pkgName, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.addAndPull(PullOptions{}, other); err != nil {
		t.Fatal(err, f.log)
	}
	want := []string{f.foreign, f.app.Layout.PackageDir("kit", pkgName, "1.0.0"), f.app.Layout.PackageDir("kit2", pkgName, "1.0.0")}
	sort.Strings(want)
	if got := f.registered(); !reflect.DeepEqual(got, want) {
		t.Errorf("registered = %v, want %v", got, want)
	}
	// The same package.json from both is no problem.
	if problems, err := f.app.Doctor(false); err != nil || len(problems) != 0 {
		t.Errorf("problems = %v, err = %v", problemMessages(problems), err)
	}
	if _, err := f.app.RemoveRecipe("kit", true); err != nil {
		t.Fatal(err, f.log)
	}
	if !exists(f.app.Layout.PackageDir("kit2", pkgName, "1.0.0")) {
		t.Error("removing kit deleted kit2's folder")
	}
}
