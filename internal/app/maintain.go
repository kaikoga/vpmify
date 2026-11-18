package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/kaikoga/vpmify/internal/alcom"
	"github.com/kaikoga/vpmify/internal/convert"
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
// two records may share a folder when two recipes provide one package (add
// refuses that, but recipes written by hand may still do it).
func (a *App) forget(st *state.State, dirs []string) (alcom.Change, error) {
	if _, err := alcom.Load(a.Layout.ALCOMSettings()); err != nil {
		return alcom.Change{}, err
	}
	if err := st.Save(a.Layout.State()); err != nil {
		return alcom.Change{}, err
	}
	change, err := a.register(st)
	if err != nil {
		return alcom.Change{}, err
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
			return change, err
		}
	}
	return change, nil
}

// Clean deletes every available version, the downloads and the work folders,
// and unregisters the versions from ALCOM. The recipes and their version
// lists stay, so pull makes the newest versions available again; another
// version has to be named again, and a project that locks a version not made
// available again
// cannot resolve it. Clean returns the versions it deletes, or would delete when apply is
// false.
func (a *App) Clean(apply bool) ([]Entry, error) {
	var out []Entry
	err := a.withLock(func() error {
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
		switch {
		case errors.Is(err, alcom.ErrNoSettings):
			a.logf("%v; nothing to unregister", err)
		case err != nil:
			return err
		}
		for _, p := range change.Removed {
			a.logf("unregistered %s", p)
		}
		for _, rs := range st.Recipes {
			rs.Packages = map[string]*state.PackageState{}
		}
		if err := st.Save(a.Layout.State()); err != nil {
			return err
		}
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
	return out, err
}

// removeFromStore deletes a folder in the store, and the package folder
// above it when that is left empty.
func (a *App) removeFromStore(dir string) error {
	a.logf("deleting %s", dir)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	parent := filepath.Dir(dir)
	if a.reconciler().Owns(parent) {
		// Fails, as wanted, when other versions are left.
		_ = os.Remove(parent)
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
		providers := map[string][]string{}
		for _, id := range ids {
			hasRecipe[id] = true
			r, err := a.loadRecipe(id)
			if err != nil {
				add(&Problem{Message: fmt.Sprintf("%s: recipe cannot be read: %v", id, err)})
				continue
			}
			providers[r.Source.Name] = append(providers[r.Source.Name], id)
			// A missing, stale or broken version list needs the network to
			// mend, so doctor only points at update.
			if _, err := a.loadIndex(r); err != nil {
				add(&Problem{Message: fmt.Sprintf("%s: %v", id, err)})
			}
		}
		for _, name := range state.SortedKeys(providers) {
			if ids := providers[name]; len(ids) > 1 {
				add(&Problem{Message: fmt.Sprintf("recipes %v all provide %s and share its folders; `vpmify remove` all but one", ids, name)})
			}
		}

		keep := map[string]bool{}
		for _, id := range state.SortedKeys(st.Recipes) {
			rs := st.Recipes[id]
			if !hasRecipe[id] {
				add(&Problem{Message: fmt.Sprintf("%s: versions are available but the recipe is gone; `vpmify remove %s` deletes them", id, id)})
			}
			for _, name := range state.SortedKeys(rs.Packages) {
				ps := rs.Packages[name]
				for _, v := range ps.SortedVersions() {
					p, inStore := a.checkVersion(id, name, v, ps)
					if p != nil {
						add(p)
					}
					// A folder in the store is reported once, by its version.
					if inStore {
						keep[a.Layout.PackageDir(name, v)] = true
					}
				}
			}
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
	want := a.Layout.PackageDir(name, v)
	resync := fmt.Sprintf("; `vpmify pull %s@%s` makes it available again", id, v)
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

// storeOrphans returns what the store holds, at the package and the version
// level, besides the folders in keep. A package folder with nothing to keep
// is returned whole. Files the OS's file browser leaves are not counted, so
// opening the store in a file browser does not make doctor fail.
func (a *App) storeOrphans(keep map[string]bool) ([]string, error) {
	var out []string
	names, err := readDirNames(a.Layout.Packages())
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if osClutter[name] {
			continue
		}
		pkgDir := filepath.Join(a.Layout.Packages(), name)
		if !isDir(pkgDir) {
			out = append(out, pkgDir)
			continue
		}
		versions, err := readDirNames(pkgDir)
		if err != nil {
			return nil, err
		}
		var orphans []string
		counted := 0
		for _, v := range versions {
			if osClutter[v] {
				continue
			}
			counted++
			if p := filepath.Join(pkgDir, v); !keep[p] {
				orphans = append(orphans, p)
			}
		}
		if len(orphans) == counted {
			out = append(out, pkgDir)
		} else {
			out = append(out, orphans...)
		}
	}
	return out, nil
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
