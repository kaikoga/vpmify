package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/kaikoga/vpmify/internal/alcom"
)

func prunedVersions(list []Pruned) []string {
	var out []string
	for _, p := range list {
		out = append(out, p.Version)
	}
	return out
}

func TestPruneKeepsNewestPlacedPerUnity(t *testing.T) {
	f := newFixture(t)
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		f.reg.add(v, "2021.3")
	}
	ctx := context.Background()
	if err := f.app.Add(ctx, f.writeRecipe(overrideNewUnity)); err != nil {
		t.Fatal(err, f.log)
	}
	if err := f.app.Sync(ctx, []string{"kit@1.0.0"}); err != nil {
		t.Fatal(err, f.log)
	}
	// A newer version upstream that sync has not placed yet does not count:
	// prune looks only at what is placed.
	f.reg.add("2.1.0", "2021.3")

	pruned, err := f.app.Prune(nil, false)
	if err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := prunedVersions(pruned), []string{"1.0.0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pruned = %v, want %v", got, want)
	}
	// Without apply nothing changes.
	if got, want := f.placed(), []string{"1.0.0@2021.3", "1.1.0@2021.3", "2.0.0@6000.2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}

	if _, err := f.app.Prune([]string{"kit"}, true); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.placed(), []string{"1.1.0@2021.3", "2.0.0@6000.2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
	if _, err := os.Stat(f.app.Layout.PackageDir(pkgName, "1.0.0")); !os.IsNotExist(err) {
		t.Errorf("1.0.0 still in the store: %v", err)
	}
	if got := f.registered(); len(got) != 3 {
		t.Errorf("registered = %v", got)
	}
}

func TestPruneDropsVersionsOutsideTheRange(t *testing.T) {
	f := newFixture(t)
	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		f.reg.add(v, "2021.3")
	}
	ctx := context.Background()
	if err := f.app.Add(ctx, f.writeRecipe(`, "select": "all"`)); err != nil {
		t.Fatal(err, f.log)
	}
	if err := os.WriteFile(f.app.recipePath("kit"), []byte(`{
  "id": "kit",
  "source": {"type": "npm", "registry": "`+registryURL+`", "name": "`+pkgName+`"},
  "select": "all",
  "versions": ">=1.1.0"
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pruned, err := f.app.Prune(nil, true)
	if err != nil {
		t.Fatal(err, f.log)
	}
	if len(pruned) != 1 || pruned[0].Version != "1.0.0" || pruned[0].Reason != "outside the recipe's versions" {
		t.Errorf("pruned = %+v", pruned)
	}
	if got, want := f.placed(), []string{"1.1.0@2021.3", "2.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
}

func TestPruneUnknownRecipe(t *testing.T) {
	f := newFixture(t)
	if _, err := f.app.Prune([]string{"nope"}, false); err == nil {
		t.Error("no error for a recipe with nothing placed")
	}
}

func problemMessages(problems []Problem) []string {
	var out []string
	for _, p := range problems {
		out = append(out, p.Message)
	}
	return out
}

func TestDoctorFindsNothingAfterSync(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.app.Add(context.Background(), f.writeRecipe("")); err != nil {
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
	if err := f.app.Add(ctx, f.writeRecipe(`, "select": "all"`)); err != nil {
		t.Fatal(err, f.log)
	}
	layout := f.app.Layout
	// 1.0.0's folder is gone, 1.1.0's package.json says another version,
	// a stray folder sits in the store, a build was left unfinished, and
	// ALCOM lost 2.0.0 and lists a store folder state.json does not have.
	if err := os.RemoveAll(layout.PackageDir(pkgName, "1.0.0")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.PackageDir(pkgName, "1.1.0"), "package.json"), []byte(`{"name": "`+pkgName+`", "version": "9.9.9"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stray := layout.PackageDir("com.example.stray", "0.1.0")
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
	if err := s.SetUserPackageFolders([]string{f.foreign, layout.PackageDir(pkgName, "1.0.0"), layout.PackageDir(pkgName, "1.1.0"), stray}); err != nil {
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
		"2.0.0 is placed but not registered",
		"ALCOM lists " + stray,
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
	if got, want := f.placed(), []string{"2.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
	wantFolders := []string{f.foreign, layout.PackageDir(pkgName, "2.0.0")}
	sort.Strings(wantFolders)
	if got := f.registered(); !reflect.DeepEqual(got, wantFolders) {
		t.Errorf("registered = %v, want %v", got, wantFolders)
	}
	for _, gone := range []string{layout.PackageDir(pkgName, "1.1.0"), filepath.Dir(stray), leftover} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", gone, err)
		}
	}
	if problems, err := f.app.Doctor(false); err != nil || len(problems) != 0 {
		t.Errorf("after fix: problems = %v, err = %v", problemMessages(problems), err)
	}

	// sync places the forgotten versions again.
	if err := f.app.Sync(ctx, nil); err != nil {
		t.Fatal(err, f.log)
	}
	if got, want := f.placed(), []string{"1.0.0@2021.3", "1.1.0@2021.3", "2.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed after sync = %v, want %v", got, want)
	}
}

func TestDoctorReportsWhatItCannotFix(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.app.Add(context.Background(), f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if err := os.Remove(f.app.recipePath("kit")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.app.recipePath("broken"), []byte(`{"id": "broken"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	problems, err := f.app.Doctor(true)
	if err != nil {
		t.Fatal(err, f.log)
	}
	msgs := problemMessages(problems)
	if len(problems) != 2 || !strings.Contains(msgs[0], "broken: recipe cannot be read") || !strings.Contains(msgs[1], "vpmify remove kit") {
		t.Errorf("problems = %v", msgs)
	}
	for _, p := range problems {
		if p.Fixed {
			t.Errorf("fixed %+v", p)
		}
	}
	// The placed version stays.
	if got, want := f.placed(), []string{"1.0.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("placed = %v, want %v", got, want)
	}
}
