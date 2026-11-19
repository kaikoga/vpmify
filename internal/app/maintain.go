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

// forget saves st, which no longer has some versions, brings ALCOM's
// registration in line with it, and then deletes the folders those versions
// had. The registration goes first so that ALCOM never lists a folder that
// is being deleted, and settings.json is read before st is saved so that a
// missing settings.json leaves everything as it was.
//
// Only folders in the store that no other record points to are deleted. A
// record may point outside the store after the user moved Vpmify's data, and
// a state.json edited by hand may have two records share a folder.
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
	change, err = a.register(st)
	if err != nil {
		return change, true, err
	}
	still := map[string]bool{}
	for _, p := range st.AllPaths() {
		still[filepath.Clean(p)] = true
	}
	owner := a.reconciler()
	for _, d := range dirs {
		clean := filepath.Clean(d)
		if !owner.Owns(clean) || still[clean] {
			a.logf("leaving %s alone: it is outside the store or another recipe uses it", d)
			continue
		}
		if err := a.removeFromStore(d); err != nil {
			return change, true, err
		}
	}
	return change, true, nil
}

// Clean deletes every available version, the downloads and the work folders,
// and unregisters the versions from ALCOM. The recipes and their version
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
					vs := ps.Versions[v]
					out = append(out, Entry{Recipe: id, Package: name, Version: v, Unity: vs.Unity, Path: vs.Path})
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

// Problem is one disagreement doctor found among the recipes, state.json,
// the store and ALCOM's settings.json.
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
	dir       string
	register  bool
}

// Doctor reports the disagreements among the recipes, state.json, the store
// and settings.json, and fixes what it can when fix is true. It does not go
// to the network; converting versions again is pull's work.
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
				add(&Problem{Message: fmt.Sprintf("%s: versions are available but the recipe is gone; `vpmify recipe remove %s` deletes them", id, id)})
			}
			for _, name := range state.SortedKeys(rs.Packages) {
				ps := rs.Packages[name]
				// Not removed on its own: a project may lock the versions.
				if r := recipes[id]; r != nil && len(ps.Versions) > 0 {
					if src, _ := r.Package(name); src == nil {
						add(&Problem{Message: fmt.Sprintf("%s: versions of %s are available but the recipe no longer provides it; `vpmify package remove --all %s/%s` deletes them", id, name, id, name)})
					}
				}
				for _, v := range ps.SortedVersions() {
					p, inStore := a.checkVersion(id, name, v, ps)
					if p != nil {
						add(p)
					}
					// A folder in the store is reported once, by its version.
					if inStore {
						keep[a.Layout.PackageDir(id, name, v)] = true
					}
				}
			}
		}
		for _, p := range differingCopies(st) {
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

		// The registration is compared with the state after the records
		// above are forgotten, since that is what --fix registers.
		forgotten := map[string]bool{}
		for _, p := range problems {
			if p.change != nil {
				p.change()
			}
			if p.forgotten != "" {
				forgotten[filepath.Clean(p.forgotten)] = true
			}
		}
		regProblems, regErr := a.checkRegistration(st, forgotten)
		if regErr != nil {
			add(&Problem{Message: fmt.Sprintf("cannot check the registration: %v", regErr)})
		}
		problems = append(problems, regProblems...)

		if !fix {
			return nil
		}
		if regErr != nil {
			// Saving the state without registering it would leave ALCOM
			// listing folders that --fix then deletes.
			return fmt.Errorf("cannot fix without ALCOM's settings: %w", regErr)
		}
		return a.fix(st, problems)
	})
	out := make([]Problem, len(problems))
	for i, p := range problems {
		out[i] = *p
	}
	return out, err
}

// checkVersion returns the problem with one available version, or nil when its
// folder holds the package state.json says it does. inStore reports whether
// the version's folder in the store is accounted for by this record.
func (a *App) checkVersion(id, name, v string, ps *state.PackageState) (p *Problem, inStore bool) {
	vs := ps.Versions[v]
	want := a.Layout.PackageDir(id, name, v)
	resync := fmt.Sprintf("; `vpmify package pull %s/%s@%s` makes it available again", id, name, v)
	forget := func() { delete(ps.Versions, v) }
	if !samePath(vs.Path, want) {
		// The user may have moved Vpmify's data. The recorded folder is
		// left alone: it is not in the store, so it is not Vpmify's.
		msg := fmt.Sprintf("%s: %s %s is recorded at %s, outside the store %s", id, name, v, vs.Path, a.Layout.Packages())
		if isDir(want) && convert.CheckManifest(want, name, v) == nil {
			return &Problem{Message: msg, Fix: "record " + want + " instead",
				change: func() { vs.Path = want }, register: true}, true
		}
		return &Problem{Message: msg, Fix: "forget the record" + resync,
			change: forget, forgotten: vs.Path, register: true}, false
	}
	if !isDir(vs.Path) {
		return &Problem{Message: fmt.Sprintf("%s: %s %s: folder %s is missing", id, name, v, vs.Path),
			Fix: "forget the record" + resync, change: forget, forgotten: vs.Path, register: true}, true
	}
	if err := convert.CheckManifest(vs.Path, name, v); err != nil {
		return &Problem{Message: fmt.Sprintf("%s: %s %s: %v", id, name, v, err),
			Fix: "delete the folder and forget the record" + resync, change: forget, forgotten: vs.Path, dir: vs.Path, register: true}, true
	}
	return nil, true
}

// samePath reports whether x and y name the same folder, ignoring case where
// the OS does (filepath.Rel compares case-insensitively on Windows).
func samePath(x, y string) bool {
	rel, err := filepath.Rel(x, y)
	return err == nil && rel == "."
}

// fix saves the state the problems' records were forgotten from, registers
// it, and deletes the problems' folders. Folders are deleted only after the
// registration succeeds, so ALCOM never lists a folder that is gone.
func (a *App) fix(st *state.State, problems []*Problem) error {
	fixable, needRegister := false, false
	for _, p := range problems {
		fixable = fixable || p.Fix != ""
		needRegister = needRegister || p.register
	}
	if !fixable {
		return nil
	}
	if err := st.Save(a.Layout.State()); err != nil {
		return err
	}
	if needRegister {
		if _, err := a.register(st); err != nil {
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

// checkRegistration returns a problem for each available version ALCOM does not
// list, and for each folder in the store that ALCOM lists but state.json
// does not have. Folders in forgotten are left out: the problem with their
// version already says --fix unregisters them.
func (a *App) checkRegistration(st *state.State, forgotten map[string]bool) ([]*Problem, error) {
	s, err := alcom.Load(a.Layout.ALCOMSettings())
	if err != nil {
		return nil, err
	}
	folders, err := s.UserPackageFolders()
	if err != nil {
		return nil, err
	}
	var out []*Problem
	registered := map[string]bool{}
	for _, f := range folders {
		registered[filepath.Clean(f)] = true
	}
	want := map[string]bool{}
	for _, p := range st.AllPaths() {
		clean := filepath.Clean(p)
		want[clean] = true
		if !registered[clean] {
			out = append(out, &Problem{Message: p + " is in the store but not registered with ALCOM", Fix: "register it", register: true})
		}
	}
	owner := a.reconciler()
	for _, f := range folders {
		if clean := filepath.Clean(f); owner.Owns(clean) && !want[clean] && !forgotten[clean] {
			out = append(out, &Problem{Message: "ALCOM lists " + f + ", which state.json does not have", Fix: "unregister it", register: true})
		}
	}
	return out, nil
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
// how the package is installed; Vpmify leaves that to ALCOM and only says so
// (kaikoga 2026-11-19).
func differingCopies(st *state.State) []*Problem {
	type copyOf struct{ recipe, manifest string }
	byVersion := map[string][]copyOf{}
	for _, id := range state.SortedKeys(st.Recipes) {
		for name, ps := range st.Recipes[id].Packages {
			for v, vs := range ps.Versions {
				data, err := os.ReadFile(filepath.Join(vs.Path, "package.json"))
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
