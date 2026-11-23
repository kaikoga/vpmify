package app

import (
	"os"
	"reflect"
	"slices"
	"testing"
)

// Syncing registers the versions Vpmify keeps that ALCOM lost, leaves a
// folder outside the store alone, and skips a record whose folder is gone.
func TestSyncRegistersWhatALCOMLost(t *testing.T) {
	f := newFixture(t)
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		f.reg.add(v, "2021.3")
	}
	if err := f.addAndPull(PullOptions{All: true}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	lost := f.app.Layout.PackageDir("kit", pkgName, "1.1.0")
	gone := f.app.Layout.PackageDir("kit", pkgName, "1.2.0")
	f.unregister(lost, gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	res, err := f.app.Sync()
	if err != nil {
		t.Fatal(err, f.log)
	}
	if !reflect.DeepEqual(res.Registered, []string{lost}) {
		t.Errorf("registered = %v, want %v", res.Registered, []string{lost})
	}
	if got, want := f.available(), []string{"1.0.0@2021.3", "1.1.0@2021.3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("available = %v, want %v", got, want)
	}
	if !slices.Contains(f.registered(), f.foreign) {
		t.Errorf("the folder outside the store is gone: %v", f.registered())
	}

	// Nothing lost: nothing to do.
	if res, err := f.app.Sync(); err != nil || len(res.Registered) != 0 {
		t.Errorf("again: %+v, %v", res, err)
	}
}
