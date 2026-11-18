// Package app ties the parts together: it reads recipes, converts versions
// into the store, and registers them with ALCOM, which makes them
// available. Vpmify does not say "place" for this, so as not to mix the
// store with a project the package is installed in (kaikoga 2026-11-18).
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
	"strconv"
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

// Draft is a recipe that ReadRecipes has read and AddDrafts has not saved
// yet, so that the GUI can show what a link would add before the user
// agrees to it.
type Draft struct {
	ID      string `json:"id"`
	Package string `json:"package"`
	// Source is where the versions come from: the Git repository's URL, or
	// "npm" and the registry.
	Source   string `json:"source"`
	Versions string `json:"versions,omitempty"`
	// Origin is the file path or URL the recipe was read from.
	Origin string `json:"origin"`
	// Replaces is true when the user already has a recipe with this id.
	Replaces bool `json:"replaces"`

	recipe *recipe.Recipe
	data   []byte
}

func (a *App) readRecipeRef(ctx context.Context, ref string) (Draft, error) {
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
		return Draft{}, err
	}
	r, err := recipe.Parse(data)
	if err != nil {
		return Draft{}, fmt.Errorf("%s: %w", ref, err)
	}
	return Draft{
		ID:       r.ID,
		Package:  r.Source.Name,
		Source:   describeSource(r.Source),
		Versions: r.Versions,
		Origin:   origin,
		Replaces: fsutil.Exists(a.recipePath(r.ID)),
		recipe:   r,
		data:     data,
	}, nil
}

func describeSource(s recipe.Source) string {
	switch s.Type {
	case recipe.SourceNPM:
		if s.Registry != "" {
			return "npm " + s.Registry
		}
		return "npm"
	case recipe.SourceGitHub:
		return s.URL
	}
	return s.Type
}

// checkDrafts refuses two drafts with one id or one package: saving both
// would keep only the last.
func checkDrafts(drafts []Draft) error {
	ids := map[string]string{}
	names := map[string]string{}
	for _, d := range drafts {
		if d.recipe == nil {
			return fmt.Errorf("recipe %q was not read by ReadRecipes", d.ID)
		}
		if prev, ok := ids[d.recipe.ID]; ok {
			return fmt.Errorf("%s and %s both have id %q", prev, d.Origin, d.recipe.ID)
		}
		if prev, ok := names[d.recipe.Source.Name]; ok {
			return fmt.Errorf("%s and %s both provide %s", prev, d.Origin, d.recipe.Source.Name)
		}
		ids[d.recipe.ID] = d.Origin
		names[d.recipe.Source.Name] = d.Origin
	}
	return nil
}

// Failure is one recipe a command could not finish while it went on with
// the others, or, with Recipe empty, a step that belongs to no one recipe.
// The command's error says the same in words.
type Failure struct {
	Recipe string `json:"recipe,omitempty"`
	Error  string `json:"error"`
}

// failures keeps the Failures of a command and the errors it returns, so
// that the error still wraps what went wrong.
type failures struct {
	list *[]Failure
	errs []error
}

func (f *failures) add(recipe string, err error) {
	*f.list = append(*f.list, Failure{Recipe: recipe, Error: err.Error()})
	if recipe != "" {
		err = fmt.Errorf("%s: %w", recipe, err)
	}
	f.errs = append(f.errs, err)
}

func (f *failures) err() error { return errors.Join(f.errs...) }

// AddResult is what Add and AddDrafts did: the recipes they saved, and the
// reading of their version lists.
type AddResult struct {
	Added []Draft `json:"added"`
	UpdateResult
}

// Add reads recipes from file paths or http(s) URLs, saves them among the
// user's recipes, and reads their version lists. It converts nothing; Pull
// does (kaikoga 2026-11-18). Nothing is saved unless every recipe reads
// well.
func (a *App) Add(ctx context.Context, refs ...string) (*AddResult, error) {
	drafts, err := a.ReadRecipes(ctx, refs...)
	if err != nil {
		return nil, err
	}
	return a.AddDrafts(ctx, drafts)
}

// ReadRecipes reads recipes from file paths or http(s) URLs as Add does,
// without saving them.
func (a *App) ReadRecipes(ctx context.Context, refs ...string) ([]Draft, error) {
	var drafts []Draft
	for _, ref := range refs {
		d, err := a.readRecipeRef(ctx, ref)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, d)
	}
	if err := checkDrafts(drafts); err != nil {
		return nil, err
	}
	return drafts, nil
}

// AddDrafts saves recipes that ReadRecipes has read, as they were read, and
// reads their version lists. Nothing is saved unless every recipe can be,
// except when writing a file fails partway. The result is nil when no
// recipe file was written.
func (a *App) AddDrafts(ctx context.Context, drafts []Draft) (*AddResult, error) {
	if err := checkDrafts(drafts); err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	names := map[string]string{}
	for _, d := range drafts {
		ids[d.recipe.ID] = true
		names[d.recipe.Source.Name] = d.Origin
	}
	var res *AddResult
	err := a.withLock(func() error {
		st, err := state.Load(a.Layout.State())
		if err != nil {
			return err
		}
		// Two recipes for one package would convert it into the same folders,
		// and removing one would delete the other's.
		existing, err := a.recipeIDs()
		if err != nil {
			return err
		}
		for _, id := range existing {
			if ids[id] {
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
		res = &AddResult{Added: []Draft{}, UpdateResult: UpdateResult{Recipes: []RecipeUpdate{}, Errors: []Failure{}}}
		for _, d := range drafts {
			if err := fsutil.WriteFileAtomic(a.recipePath(d.recipe.ID), d.data, 0o644); err != nil {
				return err
			}
			res.Added = append(res.Added, d)
			st.Recipe(d.recipe.ID).Origin = d.Origin
			a.logf("%s: added recipe", d.recipe.ID)
			addedIDs = append(addedIDs, d.recipe.ID)
		}
		if err := st.Save(a.Layout.State()); err != nil {
			return err
		}
		up, err := a.update(ctx, addedIDs)
		if up != nil {
			res.UpdateResult = *up
		}
		return err
	})
	if res != nil && len(res.Added) == 0 {
		res = nil
	}
	return res, err
}

// UpdateResult is what Update did.
type UpdateResult struct {
	Recipes []RecipeUpdate `json:"recipes"`
	// Errors are the recipes whose version lists could not be read.
	Errors []Failure `json:"errors"`
}

// RecipeUpdate is one version list Update read.
type RecipeUpdate struct {
	ID string `json:"id"`
	// Versions is how many versions the list has: those in the recipe's
	// range.
	Versions int `json:"versions"`
	// Upstream is how many versions the source has.
	Upstream int `json:"upstream"`
	// New are the versions the last list did not have. It is empty when
	// there was no list for what the recipe is now.
	New []string `json:"new"`
}

// Update reads the version lists of the named recipes (all recipes when
// ids is empty) from upstream and keeps them for Pull and Versions. It
// downloads no archive and changes no available version. The result is nil
// when Update stopped before reading any list.
func (a *App) Update(ctx context.Context, ids []string) (*UpdateResult, error) {
	var res *UpdateResult
	err := a.withLock(func() error {
		var err error
		res, err = a.update(ctx, ids)
		return err
	})
	return res, err
}

func (a *App) update(ctx context.Context, ids []string) (*UpdateResult, error) {
	if len(ids) == 0 {
		all, err := a.recipeIDs()
		if err != nil {
			return nil, err
		}
		if len(all) == 0 {
			a.logf("no recipes; add one with `vpmify add <recipe.json>`")
		}
		ids = all
	}
	res := &UpdateResult{Recipes: []RecipeUpdate{}, Errors: []Failure{}}
	fails := failures{list: &res.Errors}
	for _, id := range ids {
		u, err := a.updateRecipe(ctx, id)
		if err != nil {
			fails.add(id, err)
			continue
		}
		res.Recipes = append(res.Recipes, u)
	}
	return res, fails.err()
}

func (a *App) updateRecipe(ctx context.Context, id string) (RecipeUpdate, error) {
	r, err := a.loadRecipe(id)
	if err != nil {
		return RecipeUpdate{}, err
	}
	src, err := source.New(r, a.Client, a.Layout.Cache())
	if err != nil {
		return RecipeUpdate{}, err
	}
	upstream, err := src.Releases(ctx)
	if err != nil {
		return RecipeUpdate{}, err
	}
	// The recipe's range is the rule for what Vpmify takes from the source,
	// so the list keeps only the versions in it (kaikoga 2026-11-18).
	var releases []source.Release
	for _, rel := range upstream {
		if r.InRange(rel.Version) {
			releases = append(releases, rel)
		}
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
		return RecipeUpdate{}, err
	}
	fresh := []string{}
	for _, rel := range releases {
		if hadPrev && !known[rel.Version.String()] {
			fresh = append(fresh, rel.Version.String())
		}
	}
	msg := fmt.Sprintf("%s: %d versions in the recipe's range (%d upstream)", id, len(releases), len(upstream))
	if len(fresh) > 0 {
		msg += "; new: " + strings.Join(fresh, ", ")
	}
	a.logf("%s", msg)
	return RecipeUpdate{ID: id, Versions: len(releases), Upstream: len(upstream), New: fresh}, nil
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
	// Refetch downloads every available version of the named recipes again
	// and builds it again, so a broken store or cache is mended without
	// changing which versions are available. A version the version list no
	// longer has is left as it is.
	Refetch bool
}

// Pull converts versions of the named recipes (all recipes when args is
// empty) from their version lists into the store, and registers every
// available version with ALCOM, which makes them available. It adds the
// newest version for each value of the "unity" field, every version with
// opt.All, and "<id>@<version>" names one more. The named recipes' available
// versions that the recipe now builds differently are built again; other
// recipes' are not, because a recipe's output depends only on the recipe
// (kaikoga 2026-11-18). Pull never removes a version, and reads no version
// list from upstream; Update does. The result is nil when Pull stopped
// before trying any recipe.
func (a *App) Pull(ctx context.Context, args []string, opt PullOptions) (*PullResult, error) {
	targets, err := parseTargets(args)
	if err != nil {
		return nil, err
	}
	var res *PullResult
	err = a.withLock(func() error {
		var err error
		res, err = a.pull(ctx, targets, opt)
		return err
	})
	return res, err
}

// PullResult is what Pull did.
type PullResult struct {
	// Built are the versions converted into the store, in the order they
	// were.
	Built []Built `json:"built"`
	// UpToDate are the recipes that had nothing to convert.
	UpToDate []string `json:"upToDate"`
	// Registered and Unregistered are the folders whose registration with
	// ALCOM changed.
	Registered   []string `json:"registered"`
	Unregistered []string `json:"unregistered"`
	// Errors are the recipes that could not be done, and the dependencies
	// that could not be made available.
	Errors []Failure `json:"errors"`
}

// Built is one version Pull converted.
type Built struct {
	Recipe  string `json:"recipe"`
	Package string `json:"package"`
	Version string `json:"version"`
	Unity   string `json:"unity,omitempty"`
	Path    string `json:"path"`
	// Origin is where the version came from upstream, such as the tag.
	Origin string `json:"origin"`
	// Dependency is true when the version was added because another
	// available version depends on it, and false when it was chosen, named
	// or built again.
	Dependency bool `json:"dependency"`
}

func (a *App) pull(ctx context.Context, targets []target, opt PullOptions) (*PullResult, error) {
	all, err := a.recipeIDs()
	if err != nil {
		return nil, err
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
		return nil, err
	}
	managed, err := a.managedNames()
	if err != nil {
		return nil, err
	}
	// fresh holds the archives downloaded again in this run; recipes that
	// share an archive (UniVRM's packages share a tag) download it once.
	var fresh map[string]bool
	if opt.Refetch {
		fresh = map[string]bool{}
	}
	res := &PullResult{Built: []Built{}, UpToDate: []string{}, Registered: []string{}, Unregistered: []string{}, Errors: []Failure{}}
	fails := failures{list: &res.Errors}
	for _, id := range order {
		req := pullRequest{choose: true, all: opt.All, extra: extra[id]}
		if err := a.pullRecipe(ctx, st, id, req, fresh, res); err != nil {
			fails.add(id, err)
		}
	}
	a.addDependencies(ctx, st, managed, res, &fails)
	// Register whatever is in the store, even when a recipe failed, so that
	// versions made available before stay usable.
	change, err := a.register(st)
	if err != nil {
		fails.add("", err)
	}
	res.Registered = append(res.Registered, change.Added...)
	res.Unregistered = append(res.Unregistered, change.Removed...)
	return res, fails.err()
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

// pullRequest says which versions pullRecipe adds. The zero value adds
// none, and only builds available versions again when they need it.
type pullRequest struct {
	// choose adds the newest version for each value of "unity", or every
	// version with all.
	choose, all bool
	// extra names more versions.
	extra []string
}

// pullRecipe makes the versions req asks for from recipe id's version list
// available, and builds available versions again when the recipe now
// builds them differently. With fresh non-nil it builds every available
// version again from a new download. It adds what it does to res.
func (a *App) pullRecipe(ctx context.Context, st *state.State, id string, req pullRequest, fresh map[string]bool, res *PullResult) error {
	r, err := a.loadRecipe(id)
	if err != nil {
		return err
	}
	idx, err := a.loadIndex(r)
	if err != nil {
		return err
	}
	releases, err := idx.SourceReleases()
	if err != nil {
		return err
	}
	byVersion := map[string]source.Release{}
	for _, rel := range releases {
		byVersion[rel.Version.String()] = rel
	}
	var selected []source.Release
	// chooseErr is returned after the named and available versions are
	// done: a recipe whose range has only prereleases still takes one named
	// outright.
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
			return fmt.Errorf("the version list has no %s: it is outside the recipe's versions, or `vpmify update %s` has not read it yet", v, id)
		}
		selected = appendRelease(selected, rel)
	}

	name := r.Source.Name
	rs := st.Recipe(id)
	ps := rs.Package(name)
	fingerprint := func(rel source.Release) string {
		return r.Fingerprint(rel.Version, rel.Dir)
	}
	// Versions made available before are built again when the recipe now
	// builds them differently.
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

	built := 0
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
		a.logf("%s: converting %s %s (%s)", id, name, v, rel.Origin)
		if err := a.build(ctx, r, rel, dir); err != nil {
			return fmt.Errorf("%s %s: %w", name, v, err)
		}
		a.noteUnwrittenDependencies(name, rel, dir)
		unity := r.Unity(rel.Version, rel.Unity)
		ps.Versions[v] = &state.VersionState{Path: dir, Fingerprint: fp, Unity: unity}
		// Recorded before saving: the folder is built, and pull registers it
		// even when saving fails.
		dependency := !req.choose && slices.Contains(req.extra, v)
		res.Built = append(res.Built, Built{Recipe: id, Package: name, Version: v, Unity: unity, Path: dir, Origin: rel.Origin, Dependency: dependency})
		if err := st.Save(a.Layout.State()); err != nil {
			return err
		}
		built++
	}
	if built == 0 && req.choose && chooseErr == nil {
		a.logf("%s: up to date (%d versions available; version list of %s)", id, len(ps.Versions), idx.Updated.Local().Format("2006-01-02 15:04"))
		res.UpToDate = append(res.UpToDate, id)
	}
	return chooseErr
}

// noteUnwrittenDependencies tells the user about the upstream UPM
// dependencies the built package does not have in vpmDependencies: ALCOM
// does not install them along with it. Unity's own packages are left out,
// because Unity resolves them from its registry.
func (a *App) noteUnwrittenDependencies(name string, rel source.Release, dir string) {
	written, err := readVPMDependencies(dir)
	if err != nil {
		return
	}
	for _, dep := range state.SortedKeys(rel.Dependencies) {
		if _, ok := written[dep]; ok || strings.HasPrefix(dep, "com.unity.") {
			continue
		}
		a.logf("  note: upstream %s depends on %s, which the recipe does not write in vpmDependencies", name, dep)
	}
}

// addDependencies makes available, for each available version whose
// vpmDependencies name a package a recipe provides, a version of that
// package that satisfies it when none is available, so that ALCOM can
// install the dependent (kaikoga 2026-11-18). A pull that names only the
// dependent would otherwise leave it pointing at nothing. What it does goes
// to res, and what it cannot do to fails.
func (a *App) addDependencies(ctx context.Context, st *state.State, managed providers, res *PullResult, fails *failures) {
	type failure struct {
		recipe string
		err    error
	}
	failed := map[string]failure{}
	tried := map[string]bool{}
	// A dependency made available may need its own dependencies, so this
	// goes on until nothing new is needed.
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
				failed[key] = failure{err: fmt.Errorf("%s needs %s %s: %w", n.dependent, n.dep, n.rng, err)}
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
			a.logf("%s: adding %s, which available versions depend on", id, strings.Join(needs[id], ", "))
			if err := a.pullRecipe(ctx, st, id, pullRequest{extra: needs[id]}, nil, res); err != nil {
				failed["pull "+id] = failure{recipe: id, err: err}
			}
		}
	}
	for _, k := range state.SortedKeys(failed) {
		fails.add(failed[k].recipe, failed[k].err)
	}
}

// unmet is one vpmDependencies entry no available version satisfies.
type unmet struct {
	dependent string // "<name> <version>" of the available version
	dep       string
	id        string // the recipe that provides dep
	rng       semver.Range
}

// unmetDependencies reads the vpmDependencies of every available version,
// as ALCOM will, and returns the entries on packages a recipe provides that
// no available version satisfies.
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
	for v := range st.AvailableVersions(id, name) {
		if pv, err := semver.Parse(v); err == nil && rng.Contains(pv) {
			return true
		}
	}
	return false
}

// dependencyVersion picks the version of recipe id to add for rng: the
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

// build converts one version in a work folder and renames it into the
// store. A version already in the store is swapped out only after the new
// one is complete, so ALCOM never sees a half-built folder at the
// registered path.
func (a *App) build(ctx context.Context, r *recipe.Recipe, rel source.Release, dir string) error {
	overrides, err := r.ExpandedOverridesFor(rel.Version, rel.Dependencies)
	if err != nil {
		return err
	}
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
	if err := convert.ApplyOverrides(pkg, overrides); err != nil {
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

// register brings ALCOM's registration in line with st, and returns what it
// changed, even when it failed partway.
func (a *App) register(st *state.State) (alcom.Change, error) {
	change, err := a.reconciler().Reconcile(st.AllPaths())
	// Reconcile counts a folder twice when it writes again after another
	// program overwrote the first write.
	change.Added = uniquePaths(change.Added)
	change.Removed = uniquePaths(change.Removed)
	for _, p := range change.Added {
		a.logf("registered %s", p)
	}
	for _, p := range change.Removed {
		a.logf("unregistered %s", p)
	}
	return change, err
}

// uniquePaths returns paths without repeats, in order, never nil.
func uniquePaths(paths []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range paths {
		if c := filepath.Clean(p); !seen[c] {
			seen[c] = true
			out = append(out, p)
		}
	}
	return out
}

// RemoveResult is what Remove did.
type RemoveResult struct {
	// Removed are the versions that are no longer available; Registered is
	// false for each.
	Removed []Entry `json:"removed"`
	// RecipeRemoved is true when the recipe itself was removed.
	RecipeRemoved bool `json:"recipeRemoved"`
	// Registered and Unregistered are the folders whose registration with
	// ALCOM changed. Remove registers the available versions ALCOM is
	// missing, as pull does.
	Registered   []string `json:"registered"`
	Unregistered []string `json:"unregistered"`
}

// Remove unregisters and deletes one version of a recipe's packages, or the
// whole recipe when version is empty. Copies already in projects stay. The
// result is nil when Remove stopped before forgetting any version.
func (a *App) Remove(ctx context.Context, id, version string) (*RemoveResult, error) {
	var res *RemoveResult
	err := a.withLock(func() error {
		st, err := state.Load(a.Layout.State())
		if err != nil {
			return err
		}
		rs, ok := st.Recipes[id]
		if !ok && version != "" {
			return fmt.Errorf("no recipe %q", id)
		}
		var dirs []string
		removed := []Entry{}
		if rs != nil {
			for _, name := range state.SortedKeys(rs.Packages) {
				ps := rs.Packages[name]
				for _, v := range ps.SortedVersions() {
					if version != "" && v != version {
						continue
					}
					vs := ps.Versions[v]
					dirs = append(dirs, vs.Path)
					removed = append(removed, Entry{Recipe: id, Package: name, Version: v, Unity: vs.Unity, Path: vs.Path})
					delete(ps.Versions, v)
				}
			}
		}
		if version != "" && len(dirs) == 0 {
			return fmt.Errorf("%s: version %s is not available", id, version)
		}
		hadRecipe := ok || fsutil.Exists(a.recipePath(id))
		if version == "" {
			delete(st.Recipes, id)
		}
		change, saved, err := a.forget(st, dirs)
		if saved {
			res = &RemoveResult{Removed: removed, Registered: change.Added, Unregistered: change.Removed}
		}
		if err != nil {
			return err
		}
		if version == "" {
			for _, p := range []string{a.recipePath(id), a.Layout.Index(id)} {
				if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
			a.logf("%s: removed recipe", id)
			res.RecipeRemoved = hadRecipe
		}
		return nil
	})
	return res, err
}

// Entry is one available version, as `vpmify list` shows it.
type Entry struct {
	Recipe     string `json:"recipe"`
	Package    string `json:"package"`
	Version    string `json:"version"`
	Unity      string `json:"unity,omitempty"`
	Path       string `json:"path"`
	Registered bool   `json:"registered"`
}

// ListResult is what List returns.
type ListResult struct {
	Entries []Entry `json:"entries"`
	// SettingsError says why ALCOM's settings.json could not be read; every
	// entry then shows as unregistered.
	SettingsError string `json:"settingsError,omitempty"`
}

// List returns every available version and whether ALCOM has it
// registered. The error is for failures that leave no list.
func (a *App) List() (*ListResult, error) {
	st, err := state.Load(a.Layout.State())
	if err != nil {
		return nil, err
	}
	res := &ListResult{}
	registered := map[string]bool{}
	if s, err := alcom.Load(a.Layout.ALCOMSettings()); err != nil {
		res.SettingsError = err.Error()
	} else if folders, err := s.UserPackageFolders(); err != nil {
		res.SettingsError = err.Error()
	} else {
		for _, f := range folders {
			registered[filepath.Clean(f)] = true
		}
	}
	res.Entries = []Entry{}
	for _, id := range state.SortedKeys(st.Recipes) {
		rs := st.Recipes[id]
		for _, name := range state.SortedKeys(rs.Packages) {
			ps := rs.Packages[name]
			for _, v := range ps.SortedVersions() {
				vs := ps.Versions[v]
				res.Entries = append(res.Entries, Entry{
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
	return res, nil
}

// RecipeInfo is one of the user's recipes.
type RecipeInfo struct {
	ID      string `json:"id"`
	Package string `json:"package,omitempty"`
	// Origin is the file path or URL the recipe was added from.
	Origin string `json:"origin,omitempty"`
	// Problem says why the recipe or its version list cannot be used, such
	// as a recipe in a form Vpmify no longer reads.
	Problem string `json:"problem,omitempty"`
}

// Recipes lists the user's recipes by id. A recipe that cannot be read is
// listed with its problem, so that it can be added again or removed.
func (a *App) Recipes() ([]RecipeInfo, error) {
	ids, err := a.recipeIDs()
	if err != nil {
		return nil, err
	}
	st, err := state.Load(a.Layout.State())
	if err != nil {
		return nil, err
	}
	out := []RecipeInfo{}
	for _, id := range ids {
		info := RecipeInfo{ID: id}
		if rs, ok := st.Recipes[id]; ok {
			info.Origin = rs.Origin
		}
		r, err := a.loadRecipe(id)
		if err == nil {
			info.Package = r.Source.Name
			_, err = a.loadIndex(r)
		}
		if err != nil {
			info.Problem = err.Error()
		}
		out = append(out, info)
	}
	return out, nil
}

// VersionInfo is one version in a recipe's version list, as `vpmify
// versions` shows it.
type VersionInfo struct {
	Version string `json:"version"`
	// Unity is the "unity" field after the recipe's overrides.
	Unity      string `json:"unity,omitempty"`
	Origin     string `json:"origin"`
	Prerelease bool   `json:"prerelease,omitempty"`
	// Newest means `vpmify pull` makes it available without being told: it
	// is the newest version for its value of "unity".
	Newest bool `json:"newest"`
	// Available means Vpmify has converted it and registered it with ALCOM.
	Available bool `json:"available"`
}

// VersionList is what `vpmify versions` shows for one recipe.
type VersionList struct {
	Recipe  string `json:"recipe"`
	Package string `json:"package"`
	// Updated is when the version list was read from upstream.
	Updated  time.Time     `json:"updated"`
	Versions []VersionInfo `json:"versions"`
}

// VersionsOptions narrow what Versions lists. With none set it lists every
// stable version in the version list (kaikoga 2026-11-18).
type VersionsOptions struct {
	// All lists prereleases too.
	All bool
	// Available lists only the versions Vpmify has made available.
	Available bool
	// Unity lists only the versions a project on this Unity can use, read
	// the way vrc-get reads "unity": as the oldest Unity the version
	// supports (vrc-get-vpm/src/lib.rs, unity_compatible). It is a Unity
	// version such as "2022.3" or "2022.3.22f1".
	Unity string
}

// Versions lists the versions in recipe id's version list, newest first.
// It does not go to the network.
func (a *App) Versions(id string, opt VersionsOptions) (*VersionList, error) {
	var project unityVersion
	if opt.Unity != "" {
		var ok bool
		if project, ok = parseUnity(opt.Unity); !ok {
			return nil, fmt.Errorf("bad Unity version %q (want such as 2022.3)", opt.Unity)
		}
	}
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
	available := st.AvailableVersions(id, r.Source.Name)
	newest := map[string]bool{}
	for _, rel := range newestPerUnity(r, acceptedReleases(r, releases)) {
		newest[rel.Version.String()] = true
	}
	out := &VersionList{Recipe: id, Package: r.Source.Name, Updated: idx.Updated, Versions: []VersionInfo{}}
	for i := len(releases) - 1; i >= 0; i-- {
		rel := releases[i]
		v := rel.Version.String()
		_, isAvailable := available[v]
		unity := r.Unity(rel.Version, rel.Unity)
		switch {
		case rel.Version.IsPrerelease() && !opt.All:
			continue
		case opt.Available && !isAvailable:
			continue
		case opt.Unity != "" && !project.canUse(unity):
			continue
		}
		out.Versions = append(out.Versions, VersionInfo{
			Version:    v,
			Unity:      unity,
			Origin:     rel.Origin,
			Prerelease: rel.Version.IsPrerelease(),
			Newest:     newest[v],
			Available:  isAvailable,
		})
	}
	return out, nil
}

// unityVersion is the major and minor version of a Unity release.
type unityVersion struct{ major, minor int }

// parseUnity reads the major and minor version of "2022.3" or
// "2022.3.22f1".
func parseUnity(s string) (unityVersion, bool) {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) < 2 {
		return unityVersion{}, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return unityVersion{}, false
	}
	return unityVersion{major, minor}, true
}

// canUse reports whether a project on Unity p can use a version whose
// "unity" field is unity. A version without a readable "unity" suits every
// Unity, as in vrc-get.
func (p unityVersion) canUse(unity string) bool {
	min, ok := parseUnity(unity)
	if !ok {
		return true
	}
	return p.major > min.major || (p.major == min.major && p.minor >= min.minor)
}
