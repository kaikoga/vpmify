// Package alcom changes the user package folders in ALCOM's settings.json.
//
// ALCOM does not coordinate with other processes when it writes
// settings.json. It caches the file for one second, and when it changes a
// setting it reloads the file if the cache is older, changes it and writes
// it back (vrc-get-gui/src/state/settings.rs, SettingsState::load_mut). It
// writes the file back when its settings page opens too, whether anything
// changed or not (environment_get_settings). A change Vpmify makes inside
// that short window is lost when ALCOM saves.
// Vpmify therefore keeps its read-change-write short, and reads the file
// again after a delay to see that its change is still there.
package alcom

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kaikoga/vpmify/internal/fsutil"
	"github.com/kaikoga/vpmify/internal/ojson"
)

const foldersKey = "userPackageFolders"

// ErrNoSettings means settings.json does not exist yet. ALCOM (or VCC)
// creates it on its first start.
var ErrNoSettings = errors.New("ALCOM settings.json not found")

// Settings is a loaded settings.json.
type Settings struct {
	path     string
	obj      *ojson.Object
	eol      string
	trailing bool
}

// Load reads settings.json.
func Load(path string) (*Settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w at %s; start ALCOM once to create it", ErrNoSettings, path)
	}
	if err != nil {
		return nil, err
	}
	obj, err := ojson.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	eol := ojson.DetectEOL(data)
	if !strings.Contains(string(data), "\n") {
		eol = osEOL()
	}
	return &Settings{path: path, obj: obj, eol: eol, trailing: ojson.HasTrailingEOL(data)}, nil
}

func osEOL() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

// UserPackageFolders returns the registered folders.
func (s *Settings) UserPackageFolders() ([]string, error) {
	var folders []string
	if _, err := s.obj.GetInto(foldersKey, &folders); err != nil {
		return nil, fmt.Errorf("%s: %s: %w", s.path, foldersKey, err)
	}
	return folders, nil
}

// SetUserPackageFolders replaces the registered folders.
func (s *Settings) SetUserPackageFolders(folders []string) error {
	if folders == nil {
		folders = []string{}
	}
	raw, err := json.Marshal(folders)
	if err != nil {
		return err
	}
	s.obj.Set(foldersKey, raw)
	return nil
}

// Save writes settings.json the way ALCOM does: two-space indent, the line
// ending the file already had, through a temporary file and a rename.
func (s *Settings) Save() error {
	out, err := s.obj.Format("  ", s.eol, s.trailing)
	if err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if info, err := os.Stat(s.path); err == nil {
		perm = info.Mode().Perm()
	}
	return fsutil.WriteFileAtomic(s.path, out, perm)
}

// Reconciler changes the folders under Owned in settings.json, leaving every
// other folder alone.
type Reconciler struct {
	Path string
	// Owned is the store root. Only folders under it are Vpmify's.
	Owned string
	// VerifyDelay is how long to wait before reading the file back. It
	// should be longer than ALCOM's one-second cache.
	VerifyDelay time.Duration
	// Log receives progress messages. It may be nil.
	Log func(format string, args ...any)

	// afterSave lets tests stand in for ALCOM writing during the delay.
	afterSave func()
}

// Change reports what Reconcile or Update did.
type Change struct {
	Added, Removed []string
}

// Empty reports whether nothing changed.
func (c Change) Empty() bool { return len(c.Added) == 0 && len(c.Removed) == 0 }

// Reconcile registers every folder in want and unregisters every other
// folder under Owned. When the change is lost to a concurrent write, it
// tries once more.
func (r *Reconciler) Reconcile(want []string) (Change, error) {
	wantSet := cleanSet(want)
	return r.run(edit{add: want, drop: func(clean string) bool { return !wantSet[clean] }})
}

// Update registers the folders in add and unregisters the folders under
// Owned that are in remove or inside one of them, leaving every other
// folder as it is. When the change is lost to a concurrent write, it tries
// once more.
func (r *Reconciler) Update(add, remove []string) (Change, error) {
	removeSet := cleanSet(remove)
	return r.run(edit{add: add, drop: func(clean string) bool {
		for p := clean; ; {
			if removeSet[p] {
				return true
			}
			parent := filepath.Dir(p)
			if parent == p {
				return false
			}
			p = parent
		}
	}})
}

// Registered returns the registered folders under Owned, cleaned.
func (r *Reconciler) Registered() ([]string, error) {
	s, err := Load(r.Path)
	if err != nil {
		return nil, err
	}
	folders, err := s.UserPackageFolders()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range folders {
		if clean := filepath.Clean(f); r.Owns(clean) {
			out = append(out, clean)
		}
	}
	return out, nil
}

// edit is a change to the folders: the folders in add are registered, and
// the folders under Owned that drop reports, given cleaned, are not.
type edit struct {
	add  []string
	drop func(clean string) bool
}

func cleanSet(paths []string) map[string]bool {
	out := map[string]bool{}
	for _, p := range paths {
		out[filepath.Clean(p)] = true
	}
	return out
}

func (r *Reconciler) run(e edit) (Change, error) {
	var total Change
	for attempt := 0; attempt < 2; attempt++ {
		change, err := r.apply(e)
		if err != nil {
			return total, err
		}
		total.Added = append(total.Added, change.Added...)
		total.Removed = append(total.Removed, change.Removed...)
		if change.Empty() {
			return total, nil
		}
		if r.VerifyDelay > 0 {
			time.Sleep(r.VerifyDelay)
		}
		if r.afterSave != nil {
			r.afterSave()
		}
		ok, err := r.matches(e)
		if err != nil {
			return total, err
		}
		if ok {
			return total, nil
		}
		r.logf("settings.json changed while Vpmify was writing it; trying again")
	}
	err := fmt.Errorf("%s: another program kept overwriting the user package folders; close ALCOM and run again", r.Path)
	// What was written was lost, maybe in part: the change is what the
	// file says now.
	kept, rerr := r.inEffect(total)
	if rerr != nil {
		return Change{}, errors.Join(err, rerr)
	}
	return kept, err
}

// inEffect is the part of c that the file has now: the folders it added
// that are registered, and the folders it removed that are not, each once.
func (r *Reconciler) inEffect(c Change) (Change, error) {
	s, err := Load(r.Path)
	if err != nil {
		return Change{}, err
	}
	current, err := s.UserPackageFolders()
	if err != nil {
		return Change{}, err
	}
	registered := cleanSet(current)
	var kept Change
	seen := map[string]bool{}
	for _, f := range c.Added {
		if clean := filepath.Clean(f); registered[clean] && !seen[clean] {
			seen[clean] = true
			kept.Added = append(kept.Added, f)
		}
	}
	seen = map[string]bool{}
	for _, f := range c.Removed {
		if clean := filepath.Clean(f); !registered[clean] && !seen[clean] {
			seen[clean] = true
			kept.Removed = append(kept.Removed, f)
		}
	}
	return kept, nil
}

func (r *Reconciler) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

func (r *Reconciler) apply(e edit) (Change, error) {
	var change Change
	s, err := Load(r.Path)
	if err != nil {
		return change, err
	}
	current, err := s.UserPackageFolders()
	if err != nil {
		return change, err
	}
	present := map[string]bool{}
	var next []string
	for _, f := range current {
		clean := filepath.Clean(f)
		if r.Owns(clean) && e.drop(clean) {
			change.Removed = append(change.Removed, f)
			continue
		}
		if present[clean] {
			continue
		}
		present[clean] = true
		next = append(next, f)
	}
	for _, w := range e.add {
		clean := filepath.Clean(w)
		if present[clean] {
			continue
		}
		present[clean] = true
		next = append(next, clean)
		change.Added = append(change.Added, clean)
	}
	if change.Empty() {
		return change, nil
	}
	if err := s.SetUserPackageFolders(next); err != nil {
		return change, err
	}
	return change, s.Save()
}

func (r *Reconciler) matches(e edit) (bool, error) {
	s, err := Load(r.Path)
	if err != nil {
		return false, err
	}
	current, err := s.UserPackageFolders()
	if err != nil {
		return false, err
	}
	seen := map[string]bool{}
	for _, f := range current {
		clean := filepath.Clean(f)
		if r.Owns(clean) && e.drop(clean) {
			return false, nil
		}
		seen[clean] = true
	}
	for w := range cleanSet(e.add) {
		if !seen[w] {
			return false, nil
		}
	}
	return true, nil
}

// Owns reports whether folder is under Owned, so Reconcile and Update may
// unregister it.
func (r *Reconciler) Owns(folder string) bool {
	rel, err := filepath.Rel(filepath.Clean(r.Owned), folder)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
