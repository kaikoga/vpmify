package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/kaikoga/vpmify/internal/alcom"
	"github.com/kaikoga/vpmify/internal/convert"
	"github.com/kaikoga/vpmify/internal/recipe"
	"github.com/kaikoga/vpmify/internal/semver"
	"github.com/kaikoga/vpmify/internal/state"
)

// forget saves st, which no longer has some versions, brings ALCOM's
// registration in line with it, and then deletes dirs. The registration
// goes first so that ALCOM never lists a folder that is being deleted.
func (a *App) forget(st *state.State, dirs []string) error {
	if err := st.Save(a.Layout.State()); err != nil {
		return err
	}
	if err := a.register(st); err != nil {
		return err
	}
	for _, d := range dirs {
		a.logf("deleting %s", d)
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	return nil
}

// Pruned is one placed version that the recipe no longer selects.
type Pruned struct {
	Recipe  string `json:"recipe"`
	Package string `json:"package"`
	Version string `json:"version"`
	Reason  string `json:"reason"`
}

// Prune finds the placed versions of the named recipes (all recipes when ids
// is empty) that the recipe's selection leaves out, and removes them when
// apply is true. It looks only at the versions already placed, not upstream:
// with "latest" it keeps the newest placed version for each "unity" value,
// so a package is never left without a version before sync places a newer
// one. Versions added by name (`sync <id>@<version>`) are pruned like any
// other. A recipe that cannot be read is skipped; doctor reports it.
func (a *App) Prune(ids []string, apply bool) ([]Pruned, error) {
	var out []Pruned
	err := a.withLock(func() error {
		st, err := state.Load(a.Layout.State())
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			ids = state.SortedKeys(st.Recipes)
		} else {
			for _, id := range ids {
				if _, ok := st.Recipes[id]; !ok {
					return fmt.Errorf("recipe %q has no placed versions", id)
				}
			}
		}
		var dirs []string
		for _, id := range ids {
			r, err := a.loadRecipe(id)
			if err != nil {
				a.logf("%s: skipped: %v", id, err)
				continue
			}
			rs := st.Recipes[id]
			for _, name := range state.SortedKeys(rs.Packages) {
				ps := rs.Packages[name]
				for _, p := range prunable(r, name, ps) {
					p.Recipe = id
					out = append(out, p)
					if apply {
						dirs = append(dirs, ps.Versions[p.Version].Path)
						delete(ps.Versions, p.Version)
					}
				}
				if apply && len(ps.Versions) == 0 {
					delete(rs.Packages, name)
				}
			}
		}
		if len(dirs) == 0 {
			return nil
		}
		return a.forget(st, dirs)
	})
	return out, err
}

// prunable returns the versions of package name that recipe r does not
// select, oldest first.
func prunable(r *recipe.Recipe, name string, ps *state.PackageState) []Pruned {
	var out []Pruned
	add := func(v, reason string) {
		out = append(out, Pruned{Package: name, Version: v, Reason: reason})
	}
	versions := ps.SortedVersions()
	if name != r.Source.Name {
		for _, v := range versions {
			add(v, "the recipe now provides "+r.Source.Name)
		}
		return out
	}
	accepted := map[string]semver.Version{}
	newest := map[string]semver.Version{}
	for _, v := range versions {
		parsed, err := semver.Parse(v)
		if err != nil {
			add(v, "not a version")
			continue
		}
		if !r.Accepts(parsed) {
			add(v, "outside the recipe's versions")
			continue
		}
		accepted[v] = parsed
		// versions is oldest first, so the last one per unity wins.
		newest[ps.Versions[v].Unity] = parsed
	}
	if r.Select == recipe.SelectLatest {
		for _, v := range versions {
			parsed, ok := accepted[v]
			if !ok {
				continue
			}
			unity := ps.Versions[v].Unity
			if n := newest[unity]; semver.Compare(parsed, n) != 0 {
				add(v, fmt.Sprintf("older than %s for unity %q", n, unity))
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return state.VersionLess(out[i].Version, out[j].Version) })
	return out
}

// Problem is one disagreement doctor found among the recipes, state.json,
// the store and ALCOM's settings.json.
type Problem struct {
	Message string `json:"message"`
	// Fix says what `doctor --fix` does about it; empty when the user has
	// to act.
	Fix   string `json:"fix,omitempty"`
	Fixed bool   `json:"fixed,omitempty"`

	forget   func()
	record   string
	dir      string
	register bool
}

// Doctor reports the disagreements among the recipes, state.json, the store
// and settings.json, and fixes what it can when fix is true. It does not go
// to the network; placing versions again is sync's work.
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
		for _, id := range ids {
			hasRecipe[id] = true
			if _, err := a.loadRecipe(id); err != nil {
				add(&Problem{Message: fmt.Sprintf("%s: recipe cannot be read: %v", id, err)})
			}
		}

		keep := map[string]bool{}
		for _, id := range state.SortedKeys(st.Recipes) {
			rs := st.Recipes[id]
			if !hasRecipe[id] {
				add(&Problem{Message: fmt.Sprintf("%s: versions are placed but the recipe is gone; `vpmify remove %s` deletes them", id, id)})
			}
			for _, name := range state.SortedKeys(rs.Packages) {
				ps := rs.Packages[name]
				for _, v := range ps.SortedVersions() {
					p := a.checkVersion(id, name, v, ps)
					if p != nil {
						add(p)
					}
					// A broken folder is reported once, by its version.
					if p == nil || p.dir != "" {
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
			if p.forget != nil {
				p.forget()
				forgotten[filepath.Clean(p.record)] = true
			}
		}
		regProblems, err := a.checkRegistration(st, forgotten)
		if err != nil {
			add(&Problem{Message: fmt.Sprintf("cannot check the registration: %v", err)})
		}
		problems = append(problems, regProblems...)

		if fix {
			return a.fix(st, problems)
		}
		return nil
	})
	out := make([]Problem, len(problems))
	for i, p := range problems {
		out[i] = *p
	}
	return out, err
}

// checkVersion returns the problem with one placed version, or nil when its
// folder holds the package state.json says it does.
func (a *App) checkVersion(id, name, v string, ps *state.PackageState) *Problem {
	vs := ps.Versions[v]
	resync := fmt.Sprintf("; `vpmify sync %s@%s` places it again", id, v)
	p := &Problem{forget: func() { delete(ps.Versions, v) }, record: vs.Path, register: true}
	switch {
	case filepath.Clean(vs.Path) != a.Layout.PackageDir(name, v):
		// Left alone: the folder is not Vpmify's to delete.
		p.Message = fmt.Sprintf("%s: %s %s is recorded at %s, outside the store %s", id, name, v, vs.Path, a.Layout.Packages())
		p.Fix = "forget the record" + resync
	case !isDir(vs.Path):
		p.Message = fmt.Sprintf("%s: %s %s: folder %s is missing", id, name, v, vs.Path)
		p.Fix = "forget the record" + resync
	default:
		err := convert.CheckManifest(vs.Path, name, v)
		if err == nil {
			return nil
		}
		p.Message = fmt.Sprintf("%s: %s %s: %v", id, name, v, err)
		p.Fix = "delete the folder and forget the record" + resync
		p.dir = vs.Path
	}
	return p
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
		if err := a.register(st); err != nil {
			return err
		}
	}
	var errs []error
	for _, p := range problems {
		if p.Fix == "" {
			continue
		}
		if p.dir != "" {
			a.logf("deleting %s", p.dir)
			if err := os.RemoveAll(p.dir); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		p.Fixed = true
	}
	return errors.Join(errs...)
}

// checkRegistration returns a problem for each placed version ALCOM does not
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
			out = append(out, &Problem{Message: p + " is placed but not registered with ALCOM", Fix: "register it", register: true})
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

// storeOrphans returns what the store holds, at the package and the version
// level, besides the folders in keep. A package folder with nothing to keep
// is returned whole.
func (a *App) storeOrphans(keep map[string]bool) ([]string, error) {
	var out []string
	names, err := readDirNames(a.Layout.Packages())
	if err != nil {
		return nil, err
	}
	for _, name := range names {
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
		for _, v := range versions {
			if p := filepath.Join(pkgDir, v); !keep[p] {
				orphans = append(orphans, p)
			}
		}
		if len(orphans) == len(versions) {
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
