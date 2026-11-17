// Package app ties the parts together: it reads recipes, places converted
// versions in the store, and registers them with ALCOM.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kaikoga/vpmify/internal/alcom"
	"github.com/kaikoga/vpmify/internal/convert"
	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/fsutil"
	"github.com/kaikoga/vpmify/internal/lock"
	"github.com/kaikoga/vpmify/internal/paths"
	"github.com/kaikoga/vpmify/internal/recipe"
	"github.com/kaikoga/vpmify/internal/semver"
	"github.com/kaikoga/vpmify/internal/source"
	"github.com/kaikoga/vpmify/internal/state"
)

// App runs Vpmify's commands.
type App struct {
	Layout paths.Layout
	Client *fetch.Client
	// Log receives progress messages.
	Log io.Writer
	// VerifyDelay is how long to wait before reading settings.json back.
	VerifyDelay time.Duration
}

func (a *App) logf(format string, args ...any) {
	if a.Log != nil {
		fmt.Fprintf(a.Log, format+"\n", args...)
	}
}

func (a *App) withLock(fn func() error) error {
	l, err := lock.Acquire(a.Layout.Lock(), func() {
		a.logf("waiting for another vpmify to finish")
	})
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer l.Release()
	return fn()
}

func (a *App) recipePath(id string) string {
	return filepath.Join(a.Layout.Recipes(), id+".json")
}

func (a *App) loadRecipe(id string) (*recipe.Recipe, error) {
	r, err := recipe.Load(a.recipePath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no recipe %q (add it with `vpmify add`)", id)
	}
	if err != nil {
		return nil, err
	}
	if r.ID != id {
		return nil, fmt.Errorf("%s: id is %q, want %q", a.recipePath(id), r.ID, id)
	}
	return r, nil
}

func (a *App) recipeIDs() ([]string, error) {
	entries, err := os.ReadDir(a.Layout.Recipes())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
	}
	return ids, nil
}

type addedRecipe struct {
	recipe *recipe.Recipe
	data   []byte
	origin string
}

func (a *App) readRecipeRef(ctx context.Context, ref string) (addedRecipe, error) {
	var data []byte
	var err error
	origin := ref
	if strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://") {
		data, err = a.Client.GetBytes(ctx, ref)
	} else {
		if abs, aerr := filepath.Abs(ref); aerr == nil {
			origin = abs
		}
		data, err = os.ReadFile(ref)
	}
	if err != nil {
		return addedRecipe{}, err
	}
	r, err := recipe.Parse(data)
	if err != nil {
		return addedRecipe{}, fmt.Errorf("%s: %w", ref, err)
	}
	return addedRecipe{recipe: r, data: data, origin: origin}, nil
}

// Add reads recipes from file paths or http(s) URLs, saves them among the
// user's recipes, and syncs all recipes. Nothing is saved unless every
// recipe reads well.
func (a *App) Add(ctx context.Context, refs ...string) error {
	var added []addedRecipe
	ids := map[string]string{}
	names := map[string]string{}
	for _, ref := range refs {
		ar, err := a.readRecipeRef(ctx, ref)
		if err != nil {
			return err
		}
		if prev, ok := ids[ar.recipe.ID]; ok {
			return fmt.Errorf("%s and %s both have id %q", prev, ref, ar.recipe.ID)
		}
		if prev, ok := names[ar.recipe.Source.Name]; ok {
			return fmt.Errorf("%s and %s both provide %s", prev, ref, ar.recipe.Source.Name)
		}
		ids[ar.recipe.ID] = ref
		names[ar.recipe.Source.Name] = ref
		added = append(added, ar)
	}
	return a.withLock(func() error {
		st, err := state.Load(a.Layout.State())
		if err != nil {
			return err
		}
		// Two recipes for one package would place it in the same folders,
		// and removing one would delete the other's.
		existing, err := a.recipeIDs()
		if err != nil {
			return err
		}
		for _, id := range existing {
			if _, replaced := ids[id]; replaced {
				continue
			}
			r, err := a.loadRecipe(id)
			if err != nil {
				continue
			}
			if ref, ok := names[r.Source.Name]; ok {
				return fmt.Errorf("%s: recipe %q already provides %s", ref, id, r.Source.Name)
			}
		}
		for _, ar := range added {
			if err := fsutil.WriteFileAtomic(a.recipePath(ar.recipe.ID), ar.data, 0o644); err != nil {
				return err
			}
			st.Recipe(ar.recipe.ID).Origin = ar.origin
			a.logf("%s: added recipe", ar.recipe.ID)
		}
		if err := st.Save(a.Layout.State()); err != nil {
			return err
		}
		// Sync every recipe: a new package may be a dependency of packages
		// placed before, which then get it in vpmDependencies.
		return a.sync(ctx, nil, false)
	})
}

type target struct {
	id      string
	version string
}

func parseTargets(args []string) ([]target, error) {
	var out []target
	for _, arg := range args {
		id, version, _ := strings.Cut(arg, "@")
		if id == "" {
			return nil, fmt.Errorf("bad target %q", arg)
		}
		if version != "" {
			if _, err := semver.Parse(version); err != nil {
				return nil, fmt.Errorf("bad target %q: %w", arg, err)
			}
		}
		out = append(out, target{id: id, version: version})
	}
	return out, nil
}

// Sync places the selected versions of the named recipes (all recipes when
// args is empty) and registers every placed version with ALCOM. An argument
// "<id>@<version>" adds that version as well. Sync never removes a version.
func (a *App) Sync(ctx context.Context, args []string) error {
	targets, err := parseTargets(args)
	if err != nil {
		return err
	}
	return a.withLock(func() error { return a.sync(ctx, targets, false) })
}

// Refetch is Sync that also downloads every placed version of the named
// recipes (all recipes when args is empty) again and builds it again, so a
// broken store or cache is mended without changing which versions are
// placed. A version upstream no longer has is left as it is.
func (a *App) Refetch(ctx context.Context, args []string) error {
	targets, err := parseTargets(args)
	if err != nil {
		return err
	}
	return a.withLock(func() error { return a.sync(ctx, targets, true) })
}

func (a *App) sync(ctx context.Context, targets []target, refetch bool) error {
	if len(targets) == 0 {
		ids, err := a.recipeIDs()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			a.logf("no recipes; add one with `vpmify add <recipe.json>`")
		}
		for _, id := range ids {
			targets = append(targets, target{id: id})
		}
	}
	extra := map[string][]string{}
	var order []string
	for _, t := range targets {
		if _, ok := extra[t.id]; !ok {
			order = append(order, t.id)
			extra[t.id] = nil
		}
		if t.version != "" {
			extra[t.id] = append(extra[t.id], t.version)
		}
	}

	st, err := state.Load(a.Layout.State())
	if err != nil {
		return err
	}
	managed, err := a.managedNames()
	if err != nil {
		return err
	}
	// fresh holds the archives downloaded again in this run; recipes that
	// share an archive (UniVRM's packages share a tag) download it once.
	var fresh map[string]bool
	if refetch {
		fresh = map[string]bool{}
	}
	var errs []error
	for _, id := range order {
		if err := a.syncRecipe(ctx, st, id, extra[id], managed, fresh); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		}
	}
	// Register whatever is in the store, even when a recipe failed, so that
	// versions placed before stay usable.
	if err := a.register(st); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// managedNames returns the package names of every recipe the user has.
// A recipe file that cannot be read is left out here; syncing it reports
// the error.
func (a *App) managedNames() (map[string]bool, error) {
	ids, err := a.recipeIDs()
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, id := range ids {
		if r, err := a.loadRecipe(id); err == nil {
			names[r.Source.Name] = true
		}
	}
	return names, nil
}

// promoted returns the UPM dependencies of rel that a recipe provides,
// sorted. They are copied into vpmDependencies, so that ALCOM installs them
// along with the package.
func promoted(r *recipe.Recipe, rel source.Release, managed map[string]bool) []string {
	var out []string
	for name := range rel.Dependencies {
		if managed[name] && name != r.Source.Name {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// syncRecipe places the versions recipe id selects. With fresh non-nil it
// also builds every placed version again from a new download.
func (a *App) syncRecipe(ctx context.Context, st *state.State, id string, extra []string, managed map[string]bool, fresh map[string]bool) error {
	r, err := a.loadRecipe(id)
	if err != nil {
		return err
	}
	src, err := source.New(r, a.Client, a.Layout.Cache())
	if err != nil {
		return err
	}
	releases, err := src.Releases(ctx)
	if err != nil {
		return err
	}
	byVersion := map[string]source.Release{}
	for _, rel := range releases {
		byVersion[rel.Version.String()] = rel
	}
	selected, err := selectReleases(r, releases)
	if err != nil {
		return err
	}
	for _, v := range extra {
		rel, ok := byVersion[v]
		if !ok {
			return fmt.Errorf("upstream has no version %s", v)
		}
		selected = appendRelease(selected, rel)
	}

	name := r.Source.Name
	rs := st.Recipe(id)
	ps := rs.Package(name)
	fingerprint := func(rel source.Release) string {
		return r.Fingerprint(rel.Version, rel.Dir, promoted(r, rel, managed))
	}
	// Versions placed before are built again when the recipe now builds
	// them differently.
	refetch := fresh != nil
	for _, v := range ps.SortedVersions() {
		rel, ok := byVersion[v]
		if !ok {
			if refetch {
				a.logf("%s: upstream no longer has %s %s; left as it is", id, name, v)
			}
			continue
		}
		if refetch || ps.Versions[v].Fingerprint != fingerprint(rel) || !fsutil.Exists(ps.Versions[v].Path) {
			selected = appendRelease(selected, rel)
		}
	}

	placed := 0
	for _, rel := range selected {
		v := rel.Version.String()
		fp := fingerprint(rel)
		dir := a.Layout.PackageDir(name, v)
		if vs, ok := ps.Versions[v]; ok && !refetch && vs.Fingerprint == fp && fsutil.Exists(vs.Path) {
			continue
		}
		if refetch && !fresh[rel.CacheKey] {
			fresh[rel.CacheKey] = true
			if err := os.Remove(filepath.Join(a.Layout.Cache(), filepath.FromSlash(rel.CacheKey))); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		a.logf("%s: placing %s %s (%s)", id, name, v, rel.Origin)
		for _, dep := range state.SortedKeys(rel.Dependencies) {
			if !managed[dep] && !strings.HasPrefix(dep, "com.unity.") {
				a.logf("  note: %s depends on %s, which no recipe provides", name, dep)
			}
		}
		if err := a.place(ctx, r, rel, promoted(r, rel, managed), dir); err != nil {
			return fmt.Errorf("%s %s: %w", name, v, err)
		}
		ps.Versions[v] = &state.VersionState{Path: dir, Fingerprint: fp, Unity: r.Unity(rel.Version, rel.Unity)}
		if err := st.Save(a.Layout.State()); err != nil {
			return err
		}
		placed++
	}
	if placed == 0 {
		a.logf("%s: up to date (%d versions placed)", id, len(ps.Versions))
	}
	return nil
}

func appendRelease(list []source.Release, rel source.Release) []source.Release {
	for _, r := range list {
		if semver.Compare(r.Version, rel.Version) == 0 {
			return list
		}
	}
	return append(list, rel)
}

// selectReleases picks the versions the recipe asks for. With "latest" it
// picks the newest version for each value of the "unity" field after
// overrides, so a project on an older Unity still finds a version it can use.
func selectReleases(r *recipe.Recipe, releases []source.Release) ([]source.Release, error) {
	var accepted []source.Release
	for _, rel := range releases {
		if r.Accepts(rel.Version) {
			accepted = append(accepted, rel)
		}
	}
	if len(accepted) == 0 {
		return nil, fmt.Errorf("no upstream version matches the recipe")
	}
	if r.Select == recipe.SelectAll {
		return accepted, nil
	}
	newest := map[string]source.Release{}
	var groups []string
	for _, rel := range accepted {
		unity := r.Unity(rel.Version, rel.Unity)
		cur, ok := newest[unity]
		if !ok {
			groups = append(groups, unity)
		}
		if !ok || semver.Compare(rel.Version, cur.Version) > 0 {
			newest[unity] = rel
		}
	}
	var out []source.Release
	for _, g := range groups {
		out = append(out, newest[g])
	}
	return out, nil
}

// place builds one version in a work folder and renames it into the store.
// A version already in the store is swapped out only after the new one is
// complete, so ALCOM never sees a half-built folder at the registered path.
func (a *App) place(ctx context.Context, r *recipe.Recipe, rel source.Release, promote []string, dir string) error {
	archive := filepath.Join(a.Layout.Cache(), filepath.FromSlash(rel.CacheKey))
	if fsutil.Exists(archive) {
		if err := rel.Digests.CheckFile(archive); err != nil {
			a.logf("cached archive is broken (%v); downloading again", err)
			_ = os.Remove(archive)
		}
	}
	if !fsutil.Exists(archive) {
		if rel.Digests.Empty() && !rel.Pinned {
			return fmt.Errorf("upstream publishes no digest for the archive")
		}
		a.logf("  downloading %s", rel.ArchiveURL)
		if err := a.Client.Download(ctx, rel.ArchiveURL, archive, rel.Digests); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(a.Layout.Tmp(), 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp(a.Layout.Tmp(), "build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	pkg := filepath.Join(work, "package")

	res, err := convert.ExtractTarGz(archive, pkg, rel.Dir)
	if err != nil {
		return err
	}
	for _, s := range res.Skipped {
		a.logf("  skipped %s (not a regular file)", s)
	}
	if err := convert.CheckManifest(pkg, r.Source.Name, rel.Version.String()); err != nil {
		return err
	}
	if err := convert.PromoteDependencies(pkg, promote); err != nil {
		return err
	}
	if err := convert.ApplyOverrides(pkg, r.OverridesFor(rel.Version)); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if fsutil.Exists(dir) {
		old := filepath.Join(work, "old")
		if err := os.Rename(dir, old); err != nil {
			return err
		}
		if err := os.Rename(pkg, dir); err != nil {
			_ = os.Rename(old, dir)
			return err
		}
		return nil
	}
	return os.Rename(pkg, dir)
}

func (a *App) reconciler() *alcom.Reconciler {
	return &alcom.Reconciler{
		Path:        a.Layout.ALCOMSettings(),
		Owned:       a.Layout.Packages(),
		VerifyDelay: a.VerifyDelay,
		Log: func(format string, args ...any) {
			a.logf(format, args...)
		},
	}
}

func (a *App) register(st *state.State) error {
	change, err := a.reconciler().Reconcile(st.AllPaths())
	if err != nil {
		return err
	}
	for _, p := range change.Added {
		a.logf("registered %s", p)
	}
	for _, p := range change.Removed {
		a.logf("unregistered %s", p)
	}
	return nil
}

// Remove unregisters and deletes one version of a recipe's packages, or the
// whole recipe when version is empty. Copies already in projects stay.
func (a *App) Remove(ctx context.Context, id, version string) error {
	return a.withLock(func() error {
		st, err := state.Load(a.Layout.State())
		if err != nil {
			return err
		}
		rs, ok := st.Recipes[id]
		if !ok && version != "" {
			return fmt.Errorf("no recipe %q", id)
		}
		var dirs []string
		if rs != nil {
			for _, name := range state.SortedKeys(rs.Packages) {
				ps := rs.Packages[name]
				for _, v := range ps.SortedVersions() {
					if version != "" && v != version {
						continue
					}
					dirs = append(dirs, ps.Versions[v].Path)
					delete(ps.Versions, v)
				}
			}
		}
		if version != "" && len(dirs) == 0 {
			return fmt.Errorf("%s: version %s is not placed", id, version)
		}
		if version == "" {
			delete(st.Recipes, id)
		}
		if err := a.forget(st, dirs); err != nil {
			return err
		}
		if version == "" {
			if err := os.Remove(a.recipePath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			a.logf("%s: removed recipe", id)
		}
		return nil
	})
}

// Entry is one placed version, as `vpmify list` shows it.
type Entry struct {
	Recipe     string `json:"recipe"`
	Package    string `json:"package"`
	Version    string `json:"version"`
	Unity      string `json:"unity,omitempty"`
	Path       string `json:"path"`
	Registered bool   `json:"registered"`
}

// List returns every placed version and whether ALCOM has it registered.
// When settings.json cannot be read, every Registered is false and
// settingsErr says why; err is for failures that leave no list.
func (a *App) List() (entries []Entry, settingsErr error, err error) {
	st, err := state.Load(a.Layout.State())
	if err != nil {
		return nil, nil, err
	}
	registered := map[string]bool{}
	if s, err := alcom.Load(a.Layout.ALCOMSettings()); err != nil {
		settingsErr = err
	} else if folders, err := s.UserPackageFolders(); err != nil {
		settingsErr = err
	} else {
		for _, f := range folders {
			registered[filepath.Clean(f)] = true
		}
	}
	out := []Entry{}
	for _, id := range state.SortedKeys(st.Recipes) {
		rs := st.Recipes[id]
		for _, name := range state.SortedKeys(rs.Packages) {
			ps := rs.Packages[name]
			for _, v := range ps.SortedVersions() {
				vs := ps.Versions[v]
				out = append(out, Entry{
					Recipe:     id,
					Package:    name,
					Version:    v,
					Unity:      vs.Unity,
					Path:       vs.Path,
					Registered: registered[filepath.Clean(vs.Path)],
				})
			}
		}
	}
	return out, settingsErr, nil
}
