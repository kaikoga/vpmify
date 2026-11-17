// Package app ties the parts together: it reads recipes, places converted
// versions in the store, and registers them with ALCOM.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kaikoga/vpmify/internal/alcom"
	"github.com/kaikoga/vpmify/internal/convert"
	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/fsutil"
	"github.com/kaikoga/vpmify/internal/index"
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
// user's recipes, and reads their version lists. It places nothing; Pull
// does (kaikoga 2026-11-18). Nothing is saved unless every recipe reads
// well.
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
		var addedIDs []string
		for _, ar := range added {
			if err := fsutil.WriteFileAtomic(a.recipePath(ar.recipe.ID), ar.data, 0o644); err != nil {
				return err
			}
			st.Recipe(ar.recipe.ID).Origin = ar.origin
			a.logf("%s: added recipe", ar.recipe.ID)
			addedIDs = append(addedIDs, ar.recipe.ID)
		}
		if err := st.Save(a.Layout.State()); err != nil {
			return err
		}
		if err := a.update(ctx, addedIDs); err != nil {
			return err
		}
		a.logf("`vpmify versions <id>` lists the versions; `vpmify pull` places the newest ones")
		return nil
	})
}

// Update reads the version lists of the named recipes (all recipes when
// ids is empty) from upstream and keeps them for Pull and Versions. It
// downloads no archive and changes nothing placed.
func (a *App) Update(ctx context.Context, ids []string) error {
	return a.withLock(func() error { return a.update(ctx, ids) })
}

func (a *App) update(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		all, err := a.recipeIDs()
		if err != nil {
			return err
		}
		if len(all) == 0 {
			a.logf("no recipes; add one with `vpmify add <recipe.json>`")
		}
		ids = all
	}
	var errs []error
	for _, id := range ids {
		if err := a.updateRecipe(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

func (a *App) updateRecipe(ctx context.Context, id string) error {
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
	// Name the versions the last list did not have, so the user sees what
	// a pull would add.
	known := map[string]bool{}
	prev, err := index.Load(a.Layout.Index(id))
	hadPrev := err == nil && prev.Key == r.ListingKey()
	if hadPrev {
		for _, e := range prev.Releases {
			known[e.Version] = true
		}
	}
	if err := index.New(r.ListingKey(), releases, time.Now()).Save(a.Layout.Index(id)); err != nil {
		return err
	}
	taken := 0
	var fresh []string
	for _, rel := range releases {
		if !r.Accepts(rel.Version) {
			continue
		}
		taken++
		if hadPrev && !known[rel.Version.String()] {
			fresh = append(fresh, rel.Version.String())
		}
	}
	msg := fmt.Sprintf("%s: %d versions the recipe takes (%d upstream)", id, taken, len(releases))
	if len(fresh) > 0 {
		msg += "; new: " + strings.Join(fresh, ", ")
	}
	a.logf("%s", msg)
	return nil
}

// loadIndex returns the version list of recipe r, refusing one read for
// what the recipe was before: it may list other folders or another source.
func (a *App) loadIndex(r *recipe.Recipe) (*index.Index, error) {
	idx, err := index.Load(a.Layout.Index(r.ID))
	if errors.Is(err, index.ErrNoIndex) {
		return nil, fmt.Errorf("no version list yet; run `vpmify update %s`", r.ID)
	}
	if err != nil {
		// update writes the list anew whatever is there, so it mends this.
		return nil, fmt.Errorf("the version list cannot be read (%w); run `vpmify update %s`", err, r.ID)
	}
	if idx.Key != r.ListingKey() {
		return nil, fmt.Errorf("the recipe changed since its version list was read; run `vpmify update %s`", r.ID)
	}
	return idx, nil
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

// PullOptions say what Pull does besides adding the newest versions.
type PullOptions struct {
	// All adds every version the recipe takes.
	All bool
	// Refetch downloads every placed version of the named recipes again and
	// builds it again, so a broken store or cache is mended without
	// changing which versions are placed. A version the version list no
	// longer has is left as it is.
	Refetch bool
}

// Pull places versions of the named recipes (all recipes when args is
// empty) from their version lists, and registers every placed version with
// ALCOM. It adds the newest version for each value of the "unity" field,
// every version with opt.All, and "<id>@<version>" names one more. It also
// builds again the placed versions of every recipe that the recipes now
// build differently, such as a package whose dependency got a recipe and
// so goes into vpmDependencies. Pull never removes a version, and reads no
// version list from upstream; Update does.
func (a *App) Pull(ctx context.Context, args []string, opt PullOptions) error {
	targets, err := parseTargets(args)
	if err != nil {
		return err
	}
	return a.withLock(func() error { return a.pull(ctx, targets, opt) })
}

func (a *App) pull(ctx context.Context, targets []target, opt PullOptions) error {
	all, err := a.recipeIDs()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		if len(all) == 0 {
			a.logf("no recipes; add one with `vpmify add <recipe.json>`")
		}
		for _, id := range all {
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
	if opt.Refetch {
		fresh = map[string]bool{}
	}
	var errs []error
	for _, id := range order {
		req := pullRequest{choose: true, all: opt.All, extra: extra[id]}
		if err := a.pullRecipe(ctx, st, id, req, managed, fresh); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		}
	}
	// The other recipes' placed versions are built again too, so that
	// pulling a new dependency fixes its dependents without naming them.
	// A recipe or version list that cannot be used is the business of a
	// pull that names it; a build that fails is this pull's failure.
	for _, id := range all {
		if _, named := extra[id]; named || !st.HasPlaced(id) {
			continue
		}
		err := a.pullRecipe(ctx, st, id, pullRequest{}, managed, nil)
		var unusable unusableError
		switch {
		case errors.As(err, &unusable):
			a.logf("note: %s: cannot check whether placed versions need building again: %v", id, err)
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		}
	}
	if err := a.placeDependencies(ctx, st, managed); err != nil {
		errs = append(errs, err)
	}
	// Register whatever is in the store, even when a recipe failed, so that
	// versions placed before stay usable.
	if err := a.register(st); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// providers maps the package name of every recipe the user has to the
// recipe's id.
type providers map[string]string

// managedNames returns the providers of every recipe the user has. A recipe
// file that cannot be read is left out here; pulling it reports the error.
func (a *App) managedNames() (providers, error) {
	ids, err := a.recipeIDs()
	if err != nil {
		return nil, err
	}
	names := providers{}
	for _, id := range ids {
		if r, err := a.loadRecipe(id); err == nil {
			names[r.Source.Name] = id
		}
	}
	return names, nil
}

// promoted returns the UPM dependencies of rel that a recipe provides,
// sorted. They are copied into vpmDependencies, so that ALCOM installs them
// along with the package.
func promoted(r *recipe.Recipe, rel source.Release, managed providers) []string {
	var out []string
	for name := range rel.Dependencies {
		if _, ok := managed[name]; ok && name != r.Source.Name {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// unusableError is a recipe or version list that cannot be used as it is,
// as opposed to a failure while placing a version.
type unusableError struct{ err error }

func (e unusableError) Error() string { return e.err.Error() }
func (e unusableError) Unwrap() error { return e.err }

// pullRequest says which versions pullRecipe adds. The zero value adds
// none, and only builds placed versions again when they need it.
type pullRequest struct {
	// choose adds the newest version for each value of "unity", or every
	// version with all.
	choose, all bool
	// extra names more versions.
	extra []string
}

// pullRecipe places the versions req asks for from recipe id's version
// list, and builds placed versions again when the recipe now builds them
// differently. With fresh non-nil it builds every placed version again
// from a new download.
func (a *App) pullRecipe(ctx context.Context, st *state.State, id string, req pullRequest, managed providers, fresh map[string]bool) error {
	r, err := a.loadRecipe(id)
	if err != nil {
		return unusableError{err}
	}
	idx, err := a.loadIndex(r)
	if err != nil {
		return unusableError{err}
	}
	releases, err := idx.SourceReleases()
	if err != nil {
		return unusableError{err}
	}
	byVersion := map[string]source.Release{}
	for _, rel := range releases {
		byVersion[rel.Version.String()] = rel
	}
	var selected []source.Release
	// chooseErr is returned after the named and placed versions are done:
	// a recipe whose range is empty still takes a version named outright.
	var chooseErr error
	if req.choose {
		accepted := acceptedReleases(r, releases)
		switch {
		case len(accepted) > 0 && req.all:
			selected = accepted
		case len(accepted) > 0:
			selected = newestPerUnity(r, accepted)
		case len(req.extra) == 0:
			chooseErr = fmt.Errorf("no version in the version list matches the recipe")
		}
	}
	for _, v := range req.extra {
		rel, ok := byVersion[v]
		if !ok {
			return fmt.Errorf("the version list has no %s (`vpmify update %s` reads it again)", v, id)
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
				a.logf("%s: the version list no longer has %s %s; left as it is", id, name, v)
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
			if _, ok := managed[dep]; !ok && !strings.HasPrefix(dep, "com.unity.") {
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
	if placed == 0 && req.choose && chooseErr == nil {
		a.logf("%s: up to date (%d versions placed; version list of %s)", id, len(ps.Versions), idx.Updated.Local().Format("2006-01-02 15:04"))
	}
	return chooseErr
}

// placeDependencies places, for each placed version whose vpmDependencies
// name a package a recipe provides, a version of that package that
// satisfies it when none is placed, so that ALCOM can install the dependent
// (kaikoga 2026-11-18). A pull that names only the dependent, or another
// recipe, would otherwise leave it pointing at nothing.
func (a *App) placeDependencies(ctx context.Context, st *state.State, managed providers) error {
	failed := map[string]error{}
	tried := map[string]bool{}
	// A placed dependency may need its own dependencies, so this goes on
	// until nothing new is needed.
	for {
		needs := map[string][]string{}
		var order []string
		for _, n := range a.unmetDependencies(st, managed) {
			key := n.dependent + " " + n.dep + " " + n.rng.String()
			if tried[key] {
				continue
			}
			tried[key] = true
			v, err := a.dependencyVersion(n.id, n.rng)
			if err != nil {
				failed[key] = fmt.Errorf("%s needs %s %s: %w", n.dependent, n.dep, n.rng, err)
				continue
			}
			if _, ok := needs[n.id]; !ok {
				order = append(order, n.id)
			}
			if !slices.Contains(needs[n.id], v) {
				needs[n.id] = append(needs[n.id], v)
			}
		}
		if len(order) == 0 {
			break
		}
		for _, id := range order {
			a.logf("%s: placing %s, which placed versions depend on", id, strings.Join(needs[id], ", "))
			if err := a.pullRecipe(ctx, st, id, pullRequest{extra: needs[id]}, managed, nil); err != nil {
				failed["pull "+id] = fmt.Errorf("%s: %w", id, err)
			}
		}
	}
	var errs []error
	for _, k := range state.SortedKeys(failed) {
		errs = append(errs, failed[k])
	}
	return errors.Join(errs...)
}

// unmet is one vpmDependencies entry no placed version satisfies.
type unmet struct {
	dependent string // "<name> <version>" of the placed version
	dep       string
	id        string // the recipe that provides dep
	rng       semver.Range
}

// unmetDependencies reads the vpmDependencies of every placed version, as
// ALCOM will, and returns the entries on packages a recipe provides that no
// placed version satisfies.
func (a *App) unmetDependencies(st *state.State, managed providers) []unmet {
	var out []unmet
	for _, id := range state.SortedKeys(st.Recipes) {
		rs := st.Recipes[id]
		for _, name := range state.SortedKeys(rs.Packages) {
			ps := rs.Packages[name]
			for _, v := range ps.SortedVersions() {
				deps, err := readVPMDependencies(ps.Versions[v].Path)
				if err != nil {
					continue // doctor reports a broken folder
				}
				for _, dep := range state.SortedKeys(deps) {
					depID, ok := managed[dep]
					if !ok {
						continue
					}
					rng, err := semver.ParseRange(deps[dep])
					if err != nil || satisfied(st, depID, dep, rng) {
						continue
					}
					out = append(out, unmet{dependent: name + " " + v, dep: dep, id: depID, rng: rng})
				}
			}
		}
	}
	return out
}

func satisfied(st *state.State, id, name string, rng semver.Range) bool {
	for v := range st.PlacedVersions(id, name) {
		if pv, err := semver.Parse(v); err == nil && rng.Contains(pv) {
			return true
		}
	}
	return false
}

// dependencyVersion picks the version of recipe id to place for rng: the
// version its lower bound names when the version list has it, which keeps
// packages released in lockstep (UniVRM's) together, or else the newest
// version that satisfies it.
func (a *App) dependencyVersion(id string, rng semver.Range) (string, error) {
	r, err := a.loadRecipe(id)
	if err != nil {
		return "", err
	}
	idx, err := a.loadIndex(r)
	if err != nil {
		return "", err
	}
	releases, err := idx.SourceReleases()
	if err != nil {
		return "", err
	}
	if lower, ok := strings.CutPrefix(strings.TrimSpace(rng.String()), ">="); ok {
		for _, rel := range releases {
			if rel.Version.String() == strings.TrimSpace(lower) {
				return rel.Version.String(), nil
			}
		}
	}
	for i := len(releases) - 1; i >= 0; i-- {
		if rng.Contains(releases[i].Version) {
			return releases[i].Version.String(), nil
		}
	}
	return "", fmt.Errorf("the version list of %s has no version that satisfies it (`vpmify update %s` reads it again)", id, id)
}

// readVPMDependencies returns the vpmDependencies of the package in dir.
func readVPMDependencies(dir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, err
	}
	var m struct {
		VPMDependencies map[string]string `json:"vpmDependencies"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m.VPMDependencies, nil
}

func appendRelease(list []source.Release, rel source.Release) []source.Release {
	for _, r := range list {
		if semver.Compare(r.Version, rel.Version) == 0 {
			return list
		}
	}
	return append(list, rel)
}

// acceptedReleases returns the releases the recipe takes, in order.
func acceptedReleases(r *recipe.Recipe, releases []source.Release) []source.Release {
	var out []source.Release
	for _, rel := range releases {
		if r.Accepts(rel.Version) {
			out = append(out, rel)
		}
	}
	return out
}

// newestPerUnity picks the newest release for each value of the "unity"
// field after overrides. vrc-get reads "unity" as the oldest Unity a
// version supports (vrc-get-vpm/src/lib.rs, unity_compatible), so the
// newest version a project on any Unity can use is among these.
func newestPerUnity(r *recipe.Recipe, accepted []source.Release) []source.Release {
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
	return out
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
			for _, p := range []string{a.recipePath(id), a.Layout.Index(id)} {
				if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
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

// VersionInfo is one version a recipe can place, as `vpmify versions`
// shows it.
type VersionInfo struct {
	Version string `json:"version"`
	// Unity is the "unity" field after the recipe's overrides.
	Unity      string `json:"unity,omitempty"`
	Origin     string `json:"origin"`
	Prerelease bool   `json:"prerelease,omitempty"`
	// Newest means `vpmify pull` places it without being told: it is the
	// newest version for its value of "unity".
	Newest bool `json:"newest"`
	Placed bool `json:"placed"`
}

// VersionList is what `vpmify versions` shows for one recipe.
type VersionList struct {
	Recipe  string `json:"recipe"`
	Package string `json:"package"`
	// Updated is when the version list was read from upstream.
	Updated  time.Time     `json:"updated"`
	Versions []VersionInfo `json:"versions"`
}

// Versions lists the versions recipe id takes, newest first, from its
// version list. It does not go to the network.
func (a *App) Versions(id string) (*VersionList, error) {
	r, err := a.loadRecipe(id)
	if err != nil {
		return nil, err
	}
	idx, err := a.loadIndex(r)
	if err != nil {
		return nil, err
	}
	releases, err := idx.SourceReleases()
	if err != nil {
		return nil, err
	}
	st, err := state.Load(a.Layout.State())
	if err != nil {
		return nil, err
	}
	placed := st.PlacedVersions(id, r.Source.Name)
	accepted := acceptedReleases(r, releases)
	newest := map[string]bool{}
	for _, rel := range newestPerUnity(r, accepted) {
		newest[rel.Version.String()] = true
	}
	out := &VersionList{Recipe: id, Package: r.Source.Name, Updated: idx.Updated, Versions: []VersionInfo{}}
	for i := len(accepted) - 1; i >= 0; i-- {
		rel := accepted[i]
		v := rel.Version.String()
		_, isPlaced := placed[v]
		out.Versions = append(out.Versions, VersionInfo{
			Version:    v,
			Unity:      r.Unity(rel.Version, rel.Unity),
			Origin:     rel.Origin,
			Prerelease: rel.Version.IsPrerelease(),
			Newest:     newest[v],
			Placed:     isPlaced,
		})
	}
	return out, nil
}
