package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kaikoga/vpmify/internal/alcom"
	"github.com/kaikoga/vpmify/internal/convert"
	"github.com/kaikoga/vpmify/internal/recipe"
	"github.com/kaikoga/vpmify/internal/state"
)

// forget saves st, which no longer has some versions, unregisters the
// folders those versions had from ALCOM, and then deletes them. The
// registration goes first so that ALCOM never lists a folder that is being
// deleted, and settings.json is read before st is saved so that a missing
// settings.json leaves everything as it was.
//
// saved is true once st is saved, from which point the versions are
// forgotten whatever fails after; change is what the registration did.
func (a *App) forget(st *state.State, dirs []string) (change alcom.Change, saved bool, err error) {
	if _, err := alcom.Load(a.Layout.ALCOMSettings()); err != nil {
		return change, false, err
	}
	if err := st.Save(a.Layout.State()); err != nil {
		return change, false, err
	}
	change, err = a.updateRegistration(nil, dirs)
	if err != nil {
		return change, true, err
	}
	for _, d := range dirs {
		if err := a.removeFromStore(d); err != nil {
			return change, true, err
		}
	}
	return change, true, nil
}

// inStore returns the folders among dirs that are in the store, which
// ALCOM may list.
func (a *App) inStore(dirs []string) []string {
	owner := a.reconciler()
	var out []string
	for _, d := range dirs {
		if owner.Owns(filepath.Clean(d)) {
			out = append(out, d)
		}
	}
	return out
}

// Clean deletes every version in the store, the downloads and the work
// folders, and unregisters the store from ALCOM. The recipes and their version
// lists stay, so pull makes the newest versions available again; another
// version has to be named again, and a project that locks a version not made
// available again
// cannot resolve it. Clean returns the versions it deletes, or would delete when apply is
// false, and whether it changed anything, which it may have done before
// failing.
func (a *App) Clean(apply bool) (entries []Entry, changed bool, err error) {
	var out []Entry
	err = a.withLock(func() error {
		st, err := state.Load(a.Layout.State())
		if err != nil {
			return err
		}
		for _, id := range state.SortedKeys(st.Recipes) {
			rs := st.Recipes[id]
			for _, name := range state.SortedKeys(rs.Packages) {
				ps := rs.Packages[name]
				for _, v := range ps.SortedVersions() {
					out = append(out, newEntry(id, name, v, a.Layout.PackageDir(id, name, v)))
				}
			}
		}
		if !apply {
			return nil
		}
		// Unregister first, so that ALCOM never lists a folder that is gone.
		// Without settings.json nothing is registered.
		change, err := a.reconciler().Reconcile(nil)
		for _, p := range change.Removed {
			a.logf("unregistered %s", p)
		}
		changed = !change.Empty()
		switch {
		case errors.Is(err, alcom.ErrNoSettings):
			a.logf("%v; nothing to unregister", err)
		case err != nil:
			return err
		}
		for _, rs := range st.Recipes {
			rs.Packages = map[string]*state.PackageState{}
		}
		if err := st.Save(a.Layout.State()); err != nil {
			return err
		}
		changed = true
		for _, d := range []string{a.Layout.Packages(), a.Layout.Cache(), a.Layout.Tmp()} {
			if !isDir(d) {
				continue
			}
			a.logf("deleting %s", d)
			if err := os.RemoveAll(d); err != nil {
				return err
			}
		}
		return nil
	})
	return out, changed, err
}

// removeFromStore deletes a folder in the store, and the package and the
// recipe folders above it when they are left empty.
func (a *App) removeFromStore(dir string) error {
	a.logf("deleting %s", dir)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	owner := a.reconciler()
	parent := filepath.Dir(dir)
	for range 2 {
		// Fails, as wanted, when something else is left.
		if !owner.Owns(parent) || os.Remove(parent) != nil {
			break
		}
		parent = filepath.Dir(parent)
	}
	return nil
}

// Problem is one disagreement doctor found among the recipes, state.json
// and the store.
type Problem struct {
	Message string `json:"message"`
	// Fix says what `doctor --fix` does about it; empty when the user has
	// to act.
	Fix   string `json:"fix,omitempty"`
	Fixed bool   `json:"fixed,omitempty"`

	// change edits the state the way --fix will save it.
	change func()
	// forgotten is the folder of a record change drops.
	forgotten string
	// dir is a folder --fix deletes.
	dir string
}

// Doctor reports the disagreements among the recipes, state.json and the
// store, and fixes what it can when fix is true. It does not go to the
// network; converting versions again is pull's work. What ALCOM lists is
// not a problem: the user may unregister a version in ALCOM, and register
// folders of their own, and a pull of the version registers it again.
func (a *App) Doctor(fix bool) ([]Problem, error) {
	var problems []*Problem
	err := a.withLock(func() error {
		st, err := state.Load(a.Layout.State())
		if err != nil {
			return err
		}
		add := func(p *Problem) { problems = append(problems, p) }

		ids, err := a.recipeIDs()
		if err != nil {
			return err
		}
		hasRecipe := map[string]bool{}
		recipes := map[string]*recipe.Recipe{}
		for _, id := range ids {
			hasRecipe[id] = true
			r, err := a.loadRecipe(id)
			if err != nil {
				add(&Problem{Message: fmt.Sprintf("%s: recipe cannot be read: %v", id, err)})
				continue
			}
			recipes[id] = r
			// A missing, stale or broken version list needs the network to
			// mend, so doctor only points at update.
			if _, err := a.loadLists(r); err != nil {
				add(&Problem{Message: fmt.Sprintf("%s: %v", id, err)})
			}
		}

		keep := map[string]bool{}
		for _, id := range state.SortedKeys(st.Recipes) {
			rs := st.Recipes[id]
			if !hasRecipe[id] {
				// package remove --all leaves the record of a gone recipe,
				// as it leaves every recipe it removes versions from.
				stored := 0
				for _, ps := range rs.Packages {
					stored += len(ps.Versions)
				}
				if stored > 0 {
					add(&Problem{Message: fmt.Sprintf("%s: versions are in the store but the recipe is gone; `vpmify recipe remove %s` deletes them", id, id)})
				} else {
					add(&Problem{Message: fmt.Sprintf("%s: the recipe is gone but its record stays; `vpmify recipe remove %s` forgets it", id, id)})
				}
			}
			for _, name := range state.SortedKeys(rs.Packages) {
				ps := rs.Packages[name]
				// Not removed on its own: a project may lock the versions.
				if r := recipes[id]; r != nil && len(ps.Versions) > 0 {
					if src, _ := r.Package(name); src == nil {
						add(&Problem{Message: fmt.Sprintf("%s: versions of %s are in the store but the recipe no longer provides it; `vpmify package remove --all %s/%s` deletes them", id, name, id, name)})
					}
				}
				for _, v := range ps.SortedVersions() {
					if p := a.checkVersion(id, name, v, ps); p != nil {
						add(p)
					}
					// A folder in the store is reported once, by its version.
					keep[a.Layout.PackageDir(id, name, v)] = true
				}
			}
		}
		for _, p := range a.differingCopies(st) {
			add(p)
		}

		orphans, err := a.storeOrphans(keep)
		if err != nil {
			return err
		}
		for _, o := range orphans {
			add(&Problem{Message: o + " is in the store but not in state.json", Fix: "delete it", dir: o})
		}
		leftovers, err := readDirNames(a.Layout.Tmp())
		if err != nil {
			return err
		}
		for _, l := range leftovers {
			p := filepath.Join(a.Layout.Tmp(), l)
			add(&Problem{Message: p + " is left from an unfinished build", Fix: "delete it", dir: p})
		}

		if !fix {
			return nil
		}
		for _, p := range problems {
			if p.change != nil {
				p.change()
			}
		}
		return a.fix(st, problems)
	})
	out := make([]Problem, len(problems))
	for i, p := range problems {
		out[i] = *p
	}
	return out, err
}

// checkVersion returns the problem with one version state.json has, or nil
// when its folder in the store holds the package.
func (a *App) checkVersion(id, name, v string, ps *state.PackageState) *Problem {
	dir := a.Layout.PackageDir(id, name, v)
	resync := fmt.Sprintf("; `vpmify package pull %s/%s@%s` makes it available again", id, name, v)
	forget := func() { delete(ps.Versions, v) }
	if !isDir(dir) {
		return &Problem{Message: fmt.Sprintf("%s: %s %s: folder %s is missing", id, name, v, dir),
			Fix: "forget the record" + resync, change: forget, forgotten: dir}
	}
	if err := convert.CheckManifest(dir, name, v); err != nil {
		return &Problem{Message: fmt.Sprintf("%s: %s %s: %v", id, name, v, err),
			Fix: "delete the folder and forget the record" + resync, change: forget, dir: dir}
	}
	return nil
}

// fix saves the state the problems' records were forgotten from,
// unregisters from ALCOM the folders it forgets or deletes, and deletes the
// problems' folders. Folders are deleted only after they are unregistered,
// so ALCOM never lists a folder that is gone; a forgotten folder that is
// missing already is unregistered for the same reason. settings.json is
// read before st is saved, so that one that cannot be read leaves
// everything as it was.
func (a *App) fix(st *state.State, problems []*Problem) error {
	fixable := false
	var dirs []string
	for _, p := range problems {
		if p.Fix == "" {
			continue
		}
		fixable = true
		for _, d := range []string{p.forgotten, p.dir} {
			if d != "" {
				dirs = append(dirs, d)
			}
		}
	}
	if !fixable {
		return nil
	}
	unregister := a.inStore(dirs)
	if len(unregister) > 0 {
		if _, err := alcom.Load(a.Layout.ALCOMSettings()); err != nil {
			return fmt.Errorf("cannot fix without ALCOM's settings: %w", err)
		}
	}
	if err := st.Save(a.Layout.State()); err != nil {
		return err
	}
	if len(unregister) > 0 {
		if _, err := a.updateRegistration(nil, unregister); err != nil {
			return err
		}
	}
	var errs []error
	for _, p := range problems {
		if p.Fix == "" {
			continue
		}
		if p.dir != "" {
			if err := a.removeFromStore(p.dir); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		p.Fixed = true
	}
	return errors.Join(errs...)
}

// osClutter are files the OS's file browser leaves in folders it shows.
var osClutter = map[string]bool{".DS_Store": true, "desktop.ini": true, "Thumbs.db": true}

// storeOrphans returns what the store holds, at the recipe, the package and
// the version level, besides the folders in keep. A folder with nothing to
// keep is returned whole. Files the OS's file browser leaves are not
// counted, so opening the store in a file browser does not make doctor fail.
func (a *App) storeOrphans(keep map[string]bool) ([]string, error) {
	out, _, err := orphansIn(a.Layout.Packages(), 3, keep)
	return out, err
}

// orphansIn returns what dir holds, depth levels down to the version
// folders, besides the folders in keep, and whether dir holds any folder to
// keep.
func orphansIn(dir string, depth int, keep map[string]bool) (out []string, kept bool, err error) {
	names, err := readDirNames(dir)
	if err != nil {
		return nil, false, err
	}
	for _, n := range names {
		if osClutter[n] {
			continue
		}
		p := filepath.Join(dir, n)
		if depth == 1 {
			if keep[p] {
				kept = true
			} else {
				out = append(out, p)
			}
			continue
		}
		if !isDir(p) {
			out = append(out, p)
			continue
		}
		sub, subKept, err := orphansIn(p, depth-1, keep)
		if err != nil {
			return nil, false, err
		}
		if subKept {
			kept = true
			out = append(out, sub...)
		} else {
			out = append(out, p)
		}
	}
	return out, kept, nil
}

// differingCopies returns a problem for each package version that two or
// more recipes make with different package.json. ALCOM tells them apart by
// name and version only, so it uses one of them, and which one depends on
// how the package is installed; Vpmify leaves that to ALCOM and only says so.
func (a *App) differingCopies(st *state.State) []*Problem {
	type copyOf struct{ recipe, manifest string }
	byVersion := map[string][]copyOf{}
	for _, id := range state.SortedKeys(st.Recipes) {
		for name, ps := range st.Recipes[id].Packages {
			for v := range ps.Versions {
				data, err := os.ReadFile(filepath.Join(a.Layout.PackageDir(id, name, v), "package.json"))
				if err != nil {
					continue // checkVersion reports it
				}
				var m any
				if json.Unmarshal(data, &m) != nil {
					continue
				}
				// encoding/json writes map keys sorted, so equal content
				// gives equal bytes.
				canonical, _ := json.Marshal(m)
				key := name + " " + v
				byVersion[key] = append(byVersion[key], copyOf{id, string(canonical)})
			}
		}
	}
	var out []*Problem
	for _, key := range state.SortedKeys(byVersion) {
		copies := byVersion[key]
		var ids []string
		differ := false
		for _, c := range copies {
			ids = append(ids, c.recipe)
			differ = differ || c.manifest != copies[0].manifest
		}
		if differ {
			out = append(out, &Problem{Message: fmt.Sprintf("%s: recipes %s make it with different package.json; ALCOM uses one of them", key, strings.Join(ids, ", "))})
		}
	}
	return out
}

func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
