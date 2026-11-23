package alcom

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeSettings(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "VRChatCreatorCompanion", "settings.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func folders(t *testing.T, path string) []string {
	t.Helper()
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.UserPackageFolders()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestReconcile(t *testing.T) {
	base := t.TempDir()
	owned := filepath.Join(base, "Vpmify", "packages")
	foreign := filepath.Join(base, "mine", "pkg")
	stale := filepath.Join(owned, "a", "0.1.0")
	keep := filepath.Join(owned, "a", "1.0.0")
	add := filepath.Join(owned, "a", "2.0.0")
	p := writeSettings(t, base, `{
  "pathToUnityExe": "",
  "userPackageFolders": [`+quote(foreign)+`, `+quote(stale)+`, `+quote(keep)+`],
  "unknownKey": {"x": 1},
  "defaultProjectPath": "/somewhere"
}`)

	r := &Reconciler{Path: p, Owned: owned}
	change, err := r.Reconcile([]string{keep, add})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(change.Added, []string{add}) || !reflect.DeepEqual(change.Removed, []string{stale}) {
		t.Errorf("change = %+v", change)
	}
	if got, want := folders(t, p), []string{foreign, keep, add}; !reflect.DeepEqual(got, want) {
		t.Errorf("folders = %v, want %v", got, want)
	}
	data, _ := os.ReadFile(p)
	text := string(data)
	order := []string{`"pathToUnityExe"`, `"userPackageFolders"`, `"unknownKey"`, `"defaultProjectPath"`}
	last := -1
	for _, k := range order {
		i := strings.Index(text, k)
		if i <= last {
			t.Errorf("key %s moved: %s", k, text)
		}
		last = i
	}

	// Nothing to do the second time.
	change, err = r.Reconcile([]string{keep, add})
	if err != nil {
		t.Fatal(err)
	}
	if !change.Empty() {
		t.Errorf("second change = %+v", change)
	}
}

func TestUpdate(t *testing.T) {
	base := t.TempDir()
	owned := filepath.Join(base, "Vpmify", "packages")
	foreign := filepath.Join(base, "mine", "pkg")
	other := filepath.Join(owned, "a", "0.1.0")
	gone := filepath.Join(owned, "a", "1.0.0")
	inside := filepath.Join(owned, "b", "1.0.0")
	add := filepath.Join(owned, "a", "2.0.0")
	p := writeSettings(t, base, `{"userPackageFolders": [`+quote(foreign)+`, `+quote(other)+`, `+quote(gone)+`, `+quote(inside)+`]}`)

	// Only the named folders and what is inside them change; a folder
	// outside Owned is never removed.
	r := &Reconciler{Path: p, Owned: owned}
	change, err := r.Update([]string{add}, []string{gone, filepath.Join(owned, "b"), foreign})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(change.Added, []string{add}) || !reflect.DeepEqual(change.Removed, []string{gone, inside}) {
		t.Errorf("change = %+v", change)
	}
	if got, want := folders(t, p), []string{foreign, other, add}; !reflect.DeepEqual(got, want) {
		t.Errorf("folders = %v, want %v", got, want)
	}
	got, err := r.Registered()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{other, add}; !reflect.DeepEqual(got, want) {
		t.Errorf("Registered = %v, want %v", got, want)
	}

	change, err = r.Update([]string{add}, []string{gone})
	if err != nil {
		t.Fatal(err)
	}
	if !change.Empty() {
		t.Errorf("second change = %+v", change)
	}
}

func TestReconcileCreatesKey(t *testing.T) {
	base := t.TempDir()
	owned := filepath.Join(base, "Vpmify", "packages")
	p := writeSettings(t, base, `{"pathToUnityExe": ""}`)
	add := filepath.Join(owned, "a", "1.0.0")
	if _, err := (&Reconciler{Path: p, Owned: owned}).Reconcile([]string{add}); err != nil {
		t.Fatal(err)
	}
	if got := folders(t, p); !reflect.DeepEqual(got, []string{add}) {
		t.Errorf("folders = %v", got)
	}
}

func TestReconcileRetriesWhenOverwritten(t *testing.T) {
	base := t.TempDir()
	owned := filepath.Join(base, "Vpmify", "packages")
	original := `{"userPackageFolders": []}`
	p := writeSettings(t, base, original)
	add := filepath.Join(owned, "a", "1.0.0")

	overwrites := 0
	r := &Reconciler{Path: p, Owned: owned}
	r.afterSave = func() {
		// ALCOM saving a copy it read before Vpmify wrote.
		if overwrites == 0 {
			overwrites++
			if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := r.Reconcile([]string{add}); err != nil {
		t.Fatal(err)
	}
	if got := folders(t, p); !reflect.DeepEqual(got, []string{add}) {
		t.Errorf("folders = %v", got)
	}

	// Overwritten every time: give up with an error.
	writeSettings(t, base, original)
	r.afterSave = func() {
		if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Reconcile([]string{add}); err == nil {
		t.Error("no error when the change keeps getting lost")
	}
}

// When the change keeps getting lost, what it reports is what the file
// says after: not the folders it added that are gone again, nor the ones
// it removed that are back.
func TestUpdateReportsWhatStaysWhenOverwritten(t *testing.T) {
	base := t.TempDir()
	owned := filepath.Join(base, "Vpmify", "packages")
	gone := filepath.Join(owned, "a", "0.9.0")
	add1 := filepath.Join(owned, "a", "1.0.0")
	add2 := filepath.Join(owned, "a", "1.1.0")
	p := writeSettings(t, base, `{"userPackageFolders": [`+quote(gone)+`]}`)
	r := &Reconciler{Path: p, Owned: owned}
	// ALCOM saving a copy that has gone back and only one of the folders
	// added, every time.
	r.afterSave = func() {
		if err := os.WriteFile(p, []byte(`{"userPackageFolders": [`+quote(gone)+`, `+quote(add1)+`]}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	change, err := r.Update([]string{add1, add2}, []string{gone})
	if err == nil || !strings.Contains(err.Error(), "kept overwriting") {
		t.Errorf("err = %v", err)
	}
	if !reflect.DeepEqual(change.Added, []string{add1}) || len(change.Removed) != 0 {
		t.Errorf("change = %+v", change)
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err == nil || !strings.Contains(err.Error(), "start ALCOM once") {
		t.Errorf("err = %v", err)
	}
}

func TestOwns(t *testing.T) {
	r := &Reconciler{Owned: filepath.FromSlash("/data/Vpmify/packages")}
	cases := map[string]bool{
		"/data/Vpmify/packages/a/1.0.0": true,
		"/data/Vpmify/packages":         false,
		"/data/Vpmify/packages2/a":      false,
		"/data/Vpmify":                  false,
		"/elsewhere/a":                  false,
	}
	for p, want := range cases {
		if got := r.Owns(filepath.FromSlash(p)); got != want {
			t.Errorf("Owns(%s) = %v, want %v", p, got, want)
		}
	}
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}
