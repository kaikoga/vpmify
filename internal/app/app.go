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
		return nil, fmt.Errorf("no recipe %q (add it with `vpmify recipe add`)", id)
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
	ID          string         `json:"id"`
	DisplayName string         `json:"displayName,omitempty"`
	Packages    []DraftPackage `json:"packages"`
	// Origin is the file path or URL the recipe was read from.
	Origin string `json:"origin"`
	// Replaces is true when the user already has a recipe with this id.
	Replaces bool `json:"replaces"`

	recipe *recipe.Recipe
	data   []byte
}

// DraftPackage is one package a Draft provides.
type DraftPackage struct {
	Name string `json:"name"`
	// Source is where the versions come from: the Git repository's URL, or
	// "npm" and the registry.
	Source   string `json:"source"`
	Versions string `json:"versions,omitempty"`
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
	d := Draft{
		ID:          r.ID,
		DisplayName: r.DisplayName,
		Packages:    []DraftPackage{},
		Origin:      origin,
		Replaces:    fsutil.Exists(a.recipePath(r.ID)),
		recipe:      r,
		data:        data,
	}
	for _, s := range r.Sources {
		for _, p := range s.Packages {
			d.Packages = append(d.Packages, DraftPackage{Name: p.Name, Source: s.Describe(), Versions: p.Versions})
		}
	}
	return d, nil
}

// checkDrafts refuses two drafts with one id: saving both would keep only
// the last. Two recipes may provide one package; each has its own folders
// (kaikoga 2026-11-19).
func checkDrafts(drafts []Draft) error {
	ids := map[string]string{}
	for _, d := range drafts {
		if d.recipe == nil {
			return fmt.Errorf("recipe %q was not read by ReadRecipes", d.ID)
		}
		if prev, ok := ids[d.recipe.ID]; ok {
			return fmt.Errorf("%s and %s both have id %q", prev, d.Origin, d.recipe.ID)
		}
		ids[d.recipe.ID] = d.Origin
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
	var res *AddResult
	err := a.withLock(func() error {
		st, err := state.Load(a.Layout.State())
		if err != nil {
			return err
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

// RecipeUpdate is the version lists Update read for one recipe. A source
// that could not be read has no packages here, and is in the errors.
type RecipeUpdate struct {
	ID       string          `json:"id"`
	Packages []PackageUpdate `json:"packages"`
	// Warnings say what the recipe needs updating for, though the version
	// lists were read (Git tags that its tagVersion reads as one version).
	Warnings []string `json:"warnings"`
}

// PackageUpdate is the version list of one package.
type PackageUpdate struct {
	Package string `json:"package"`
	// Versions is how many versions the list has: those in the package's
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
			a.logf("no recipes; add one with `vpmify recipe add <recipe.json>`")
		}
		ids = all
	}
	res := &UpdateResult{Recipes: []RecipeUpdate{}, Errors: []Failure{}}
	fails := failures{list: &res.Errors}
	for _, id := range ids {
		u, err := a.updateRecipe(ctx, id)
		if len(u.Packages) > 0 {
			res.Recipes = append(res.Recipes, u)
		}
		if err != nil {
			fails.add(id, err)
		}
	}
	return res, fails.err()
}

// updateRecipe reads the version list of each of recipe id's sources. A
// source that fails keeps the list it had, if it is still for what the
// source is, and the others go on (kaikoga 2026-11-19).
func (a *App) updateRecipe(ctx context.Context, id string) (RecipeUpdate, error) {
	u := RecipeUpdate{ID: id, Packages: []PackageUpdate{}, Warnings: []string{}}
	r, err := a.loadRecipe(id)
	if err != nil {
		return u, err
	}
	// update writes the index anew whatever is there, so a broken one is
	// only left unused.
	prev, _ := index.Load(a.Layout.Index(id))
	idx := &index.Index{Sources: []*index.List{}}
	var errs []error
	for _, s := range r.Sources {
		key := s.ListingKey()
		old := prev.List(key)
		l, ups, warnings, err := a.readSource(ctx, id, s, old)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.Describe(), err))
			if old != nil {
				idx.Sources = append(idx.Sources, old)
			}
			continue
		}
		idx.Sources = append(idx.Sources, l)
		u.Packages = append(u.Packages, ups...)
		u.Warnings = append(u.Warnings, warnings...)
	}
	if err := idx.Save(a.Layout.Index(id)); err != nil {
		errs = append(errs, err)
	}
	return u, errors.Join(errs...)
}

// readSource reads the version list of source s from upstream. old is the
// list the last update kept for it, if any.
func (a *App) readSource(ctx context.Context, id string, s *recipe.Source, old *index.List) (*index.List, []PackageUpdate, []string, error) {
	src, err := source.New(s, a.Client, a.Layout.Cache())
	if err != nil {
		return nil, nil, nil, err
	}
	upstream, warnings, err := src.Releases(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, w := range warnings {
		a.logf("%s: warning: %s", id, w)
	}
	if warnings == nil {
		warnings = []string{}
	}
	kept := map[string][]source.Release{}
	var ups []PackageUpdate
	for _, p := range s.Packages {
		// The package's range is the rule for what Vpmify takes from the
		// source, so the list keeps only the versions in it (kaikoga
		// 2026-11-18).
		releases := []source.Release{}
		for _, rel := range upstream[p.Name] {
			if p.InRange(rel.Version) {
				releases = append(releases, rel)
			}
		}
		kept[p.Name] = releases
		// Name the versions the last list did not have, so the user sees
		// what a pull would add.
		fresh := []string{}
		if old != nil {
			known := map[string]bool{}
			for _, e := range old.Packages[p.Name] {
				known[e.Version] = true
			}
			for _, rel := range releases {
				if !known[rel.Version.String()] {
					fresh = append(fresh, rel.Version.String())
				}
			}
		}
		msg := fmt.Sprintf("%s: %s: %d versions in the package's range (%d upstream)", id, p.Name, len(releases), len(upstream[p.Name]))
		if len(fresh) > 0 {
			msg += "; new: " + strings.Join(fresh, ", ")
		}
		a.logf("%s", msg)
		ups = append(ups, PackageUpdate{Package: p.Name, Versions: len(releases), Upstream: len(upstream[p.Name]), New: fresh})
	}
	return index.NewList(s.ListingKey(), kept, time.Now()), ups, warnings, nil
}

// target is one argument of pull: "<id>", "<id>/<name>", and either with
// "@<version>".
type target struct {
	id, name, version string
}

func parseTargets(args []string) ([]target, error) {
	var out []target
	for _, arg := range args {
		ref, version, _ := strings.Cut(arg, "@")
		id, name, err := parseRef(ref)
		if err != nil {
			return nil, fmt.Errorf("bad target %q: %w", arg, err)
		}
		if version != "" {
			if _, err := semver.Parse(version); err != nil {
				return nil, fmt.Errorf("bad target %q: %w", arg, err)
			}
		}
		out = append(out, target{id: id, name: name, version: version})
	}
	return out, nil
}

// parseRef reads "<id>" or "<id>/<name>". Neither a recipe id nor a
// package name holds "/", so the form is not ambiguous.
func parseRef(ref string) (id, name string, err error) {
	id, name, hasName := strings.Cut(ref, "/")
	if id == "" || (hasName && (name == "" || strings.Contains(name, "/"))) {
		return "", "", fmt.Errorf("want <recipe> or <recipe>/<package>, not %q", ref)
	}
	return id, name, nil
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
// newest version of each package that is not onDemand (of the package
// "<id>/<name>" names), whatever Unity it needs, every version with opt.All, and
// "<id>@<version>" names one more of every package that has it
// ("<id>/<name>@<version>" of that package). The named recipes' available
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
			a.logf("no recipes; add one with `vpmify recipe add <recipe.json>`")
		}
		for _, id := range all {
			targets = append(targets, target{id: id})
		}
	}
	reqs := map[string]*pullRequest{}
	var order []string
	for _, t := range targets {
		req, ok := reqs[t.id]
		if !ok {
			req = &pullRequest{choose: true, all: opt.All, only: map[string]bool{}}
			reqs[t.id] = req
			order = append(order, t.id)
		}
		// A target without a package name chooses from every package.
		if t.name == "" {
			req.whole = true
		} else {
			req.only[t.name] = true
		}
		if t.version != "" {
			req.extra = append(req.extra, pkgVersion{name: t.name, version: t.version})
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
		if err := a.pullRecipe(ctx, st, id, *reqs[id], fresh, res); err != nil {
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

// providers maps each package name to the recipes that provide it, in id
// order. Several recipes may provide one package (kaikoga 2026-11-19).
type providers map[string][]string

// managedNames returns the providers of every package the user's recipes
// provide. A recipe file that cannot be read is left out here; pulling it
// reports the error.
func (a *App) managedNames() (providers, error) {
	ids, err := a.recipeIDs()
	if err != nil {
		return nil, err
	}
	names := providers{}
	for _, id := range ids {
		if r, err := a.loadRecipe(id); err == nil {
			for _, n := range r.Names() {
				names[n] = append(names[n], id)
			}
		}
	}
	return names, nil
}

// pkgVersion names one version of one package. An empty name means every
// package of the recipe that has the version.
type pkgVersion struct {
	name, version string
}

// pullRequest says which versions pullRecipe adds. The zero value adds
// none, and only builds available versions again when they need it.
type pullRequest struct {
	// choose adds the newest version, or every version with all, of the
	// packages in only, and when whole is true of every package that is not
	// onDemand.
	choose, all bool
	whole       bool
	only        map[string]bool
	// extra names more versions.
	extra []pkgVersion
}

func (req pullRequest) chooses(p *recipe.Package) bool {
	return req.choose && (req.only[p.Name] || req.whole && !p.OnDemand)
}

// pullRecipe makes the versions req asks for from recipe id's version lists
// available, and builds available versions again when the recipe now
// builds them differently. With fresh non-nil it builds every available
// version again from a new download. It adds what it does to res. A source
// without a usable version list is an error, and the packages of the
// others are still done.
func (a *App) pullRecipe(ctx context.Context, st *state.State, id string, req pullRequest, fresh map[string]bool, res *PullResult) error {
	r, err := a.loadRecipe(id)
	if err != nil {
		return err
	}
	lists, listErr := a.loadLists(r)
	if len(lists) == 0 && listErr != nil {
		return listErr
	}
	// What the user names wrongly stops the recipe before anything is
	// built.
	for name := range req.only {
		if src, _ := r.Package(name); src == nil {
			return fmt.Errorf("the recipe has no package %s", name)
		}
	}
	extra := map[string][]string{}
	for _, e := range req.extra {
		if e.name != "" {
			if src, _ := r.Package(e.name); src == nil {
				return fmt.Errorf("the recipe has no package %s", e.name)
			}
			if find(lists, e.name) == nil {
				return listErr
			}
			extra[e.name] = append(extra[e.name], e.version)
			continue
		}
		// "<id>@<version>" names the version of every package that has it
		// and can convert it.
		matched := false
		var unusable []error
		for _, l := range lists {
			rel, ok := l.byVersion[e.version]
			if !ok {
				continue
			}
			if _, err := l.expand(rel); err != nil {
				unusable = append(unusable, fmt.Errorf("%s: %s: %w", l.pkg.Name, e.version, err))
				continue
			}
			extra[l.pkg.Name] = append(extra[l.pkg.Name], e.version)
			matched = true
		}
		switch {
		case !matched && len(unusable) > 0:
			return errors.Join(unusable...)
		case !matched:
			return errors.Join(listErr, fmt.Errorf("no version list has %s: it is outside the packages' versions, or `vpmify recipe update %s` has not read it yet", e.version, id))
		}
	}
	for _, l := range lists {
		for _, v := range extra[l.pkg.Name] {
			rel, ok := l.byVersion[v]
			if !ok {
				return fmt.Errorf("%s: the version list has no %s: it is outside the package's versions, or `vpmify recipe update %s` has not read it yet", l.pkg.Name, v, id)
			}
			if _, err := l.expand(rel); err != nil {
				return fmt.Errorf("%s: %s: %w", l.pkg.Name, v, err)
			}
		}
	}
	var errs []error
	// A source without a list fails the pull when the user chose from its
	// packages, not when a dependency or another package was asked for.
	if listErr != nil && req.choose {
		relevant := req.whole
		for name := range req.only {
			relevant = relevant || find(lists, name) == nil
		}
		if relevant {
			errs = append(errs, listErr)
		}
	}
	built := 0
	for _, l := range lists {
		n, err := a.pullPackage(ctx, st, id, l, req.chooses(l.pkg), req.all, req.choose, extra[l.pkg.Name], fresh, res)
		built += n
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", l.pkg.Name, err))
		}
	}
	if built == 0 && req.choose && len(errs) == 0 {
		available := 0
		for _, ps := range st.Recipe(id).Packages {
			available += len(ps.Versions)
		}
		a.logf("%s: up to date (%d versions available)", id, available)
		res.UpToDate = append(res.UpToDate, id)
	}
	return errors.Join(errs...)
}

// pullPackage makes the versions of one package available: the newest when
// choose is true (every version with all), the
// versions in extra, and the available versions the recipe now builds
// differently. byUser says the request came from the user rather than from
// a dependency. It returns how many versions it converted.
func (a *App) pullPackage(ctx context.Context, st *state.State, id string, l *pkgList, choose, all, byUser bool, extra []string, fresh map[string]bool, res *PullResult) (int, error) {
	name := l.pkg.Name
	var selected []source.Release
	// chooseErr is returned after the named and available versions are
	// done: a package whose range has only prereleases still takes one
	// named outright.
	var chooseErr error
	if choose {
		accepted := l.accepted()
		switch {
		case len(accepted) > 0 && all:
			selected = accepted
		case len(accepted) > 0:
			selected = []source.Release{newest(accepted)}
		case len(extra) == 0:
			chooseErr = fmt.Errorf("no version in the version list matches the recipe")
		}
	}
	for _, v := range extra {
		rel, ok := l.byVersion[v]
		if !ok {
			return 0, fmt.Errorf("the version list has no %s: it is outside the package's versions, or `vpmify recipe update %s` has not read it yet", v, id)
		}
		selected = appendRelease(selected, rel)
	}

	ps := st.Recipe(id).Package(name)
	// Versions made available before are built again when the recipe now
	// builds them differently.
	refetch := fresh != nil
	for _, v := range ps.SortedVersions() {
		rel, ok := l.byVersion[v]
		if !ok {
			if refetch {
				a.logf("%s: the version list no longer has %s %s; left as it is", id, name, v)
			}
			continue
		}
		expanded, err := l.expand(rel)
		if err != nil {
			a.logf("%s: %s %s cannot be built again (%v); left as it is", id, name, v, err)
			continue
		}
		if refetch || ps.Versions[v].Fingerprint != l.fingerprint(rel, expanded) || !fsutil.Exists(ps.Versions[v].Path) {
			selected = appendRelease(selected, rel)
		}
	}

	built := 0
	for _, rel := range selected {
		v := rel.Version.String()
		expanded, err := l.expand(rel)
		if err != nil {
			return built, fmt.Errorf("%s: %w", v, err)
		}
		fp := l.fingerprint(rel, expanded)
		dir := a.Layout.PackageDir(id, name, v)
		if vs, ok := ps.Versions[v]; ok && !refetch && vs.Fingerprint == fp && fsutil.Exists(vs.Path) {
			continue
		}
		if refetch {
			keys := []string{rel.CacheKey}
			for _, o := range rel.Overlays {
				keys = append(keys, o.CacheKey)
			}
			for _, key := range keys {
				if fresh[key] {
					continue
				}
				fresh[key] = true
				if err := os.Remove(filepath.Join(a.Layout.Cache(), filepath.FromSlash(key))); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return built, err
				}
			}
		}
		a.logf("%s: converting %s %s (%s)", id, name, v, rel.Origin)
		if err := a.build(ctx, name, rel, expanded, dir); err != nil {
			return built, fmt.Errorf("%s: %w", v, err)
		}
		a.noteUnwrittenDependencies(name, rel, dir)
		unity := l.pkg.Unity(rel.Version, rel.Unity)
		ps.Versions[v] = &state.VersionState{Path: dir, Fingerprint: fp, Unity: unity}
		// Recorded before saving: the folder is built, and pull registers it
		// even when saving fails.
		dependency := !byUser && slices.Contains(extra, v)
		res.Built = append(res.Built, Built{Recipe: id, Package: name, Version: v, Unity: unity, Path: dir, Origin: rel.Origin, Dependency: dependency})
		if err := st.Save(a.Layout.State()); err != nil {
			return built, err
		}
		built++
	}
	return built, chooseErr
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
		needs := map[string][]pkgVersion{}
		var order []string
		for _, n := range a.unmetDependencies(st, managed) {
			key := n.recipe + " " + n.dependent + " " + n.dep + " " + n.rng.String()
			if tried[key] {
				continue
			}
			tried[key] = true
			id, v, err := a.dependencyProvider(n, managed)
			if err != nil {
				failed[key] = failure{err: fmt.Errorf("%s needs %s %s: %w", n.dependent, n.dep, n.rng, err)}
				continue
			}
			if _, ok := needs[id]; !ok {
				order = append(order, id)
			}
			want := pkgVersion{name: n.dep, version: v}
			if !slices.Contains(needs[id], want) {
				needs[id] = append(needs[id], want)
			}
		}
		if len(order) == 0 {
			break
		}
		for _, id := range order {
			var names []string
			for _, pv := range needs[id] {
				names = append(names, pv.name+" "+pv.version)
			}
			a.logf("%s: adding %s, which available versions depend on", id, strings.Join(names, ", "))
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
	recipe    string // the recipe of the available version
	dependent string // "<name> <version>" of the available version
	dep       string
	rng       semver.Range
}

// unmetDependencies reads the vpmDependencies of every available version,
// as ALCOM will, and returns the entries on packages a recipe provides that
// no available version satisfies, from whichever recipe: ALCOM resolves
// them among everything registered.
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
					if len(managed[dep]) == 0 {
						continue
					}
					rng, err := semver.ParseRange(deps[dep])
					if err != nil || satisfied(st, dep, rng) {
						continue
					}
					out = append(out, unmet{recipe: id, dependent: name + " " + v, dep: dep, rng: rng})
				}
			}
		}
	}
	return out
}

func satisfied(st *state.State, name string, rng semver.Range) bool {
	for _, rs := range st.Recipes {
		ps, ok := rs.Packages[name]
		if !ok {
			continue
		}
		for v := range ps.Versions {
			if pv, err := semver.Parse(v); err == nil && rng.Contains(pv) {
				return true
			}
		}
	}
	return false
}

// dependencyProvider picks the recipe and the version to add for n: the
// dependent's own recipe first, and then the others that provide the
// package in id order, the first whose version list has a version that
// satisfies it (kaikoga 2026-11-19). A recipe whose version list cannot be
// used is passed over, and the user is told.
func (a *App) dependencyProvider(n unmet, managed providers) (string, string, error) {
	candidates := managed[n.dep]
	if slices.Contains(candidates, n.recipe) {
		candidates = append([]string{n.recipe}, slices.DeleteFunc(slices.Clone(candidates), func(id string) bool { return id == n.recipe })...)
	}
	var errs []error
	for _, id := range candidates {
		v, err := a.dependencyVersion(id, n.dep, n.rng)
		if err == nil {
			return id, v, nil
		}
		if errors.Is(err, errNoList) {
			a.logf("%s: passed over for %s %s: %v", id, n.dep, n.rng, err)
		}
		errs = append(errs, fmt.Errorf("%s: %w", id, err))
	}
	return "", "", errors.Join(errs...)
}

// dependencyVersion picks the version of package name of recipe id to add
// for rng: the version its lower bound names when the version list has it,
// which keeps packages released in lockstep (UniVRM's) together, or else
// the newest version that satisfies it.
func (a *App) dependencyVersion(id, name string, rng semver.Range) (string, error) {
	r, err := a.loadRecipe(id)
	if err != nil {
		return "", err
	}
	lists, listErr := a.loadLists(r)
	l := find(lists, name)
	if l == nil {
		if listErr != nil {
			return "", listErr
		}
		return "", fmt.Errorf("the recipe has no package %s", name)
	}
	var usable []source.Release
	for _, rel := range l.releases {
		if l.usable(rel) {
			usable = append(usable, rel)
		}
	}
	if lower, ok := strings.CutPrefix(strings.TrimSpace(rng.String()), ">="); ok {
		for _, rel := range usable {
			if rel.Version.String() == strings.TrimSpace(lower) {
				return rel.Version.String(), nil
			}
		}
	}
	for i := len(usable) - 1; i >= 0; i-- {
		if rng.Contains(usable[i].Version) {
			return usable[i].Version.String(), nil
		}
	}
	return "", fmt.Errorf("the version list of %s has no version that satisfies it (`vpmify recipe update %s` reads it again)", name, id)
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

// build converts one version of package name in a work folder, with the
// overrides as expanded for it, and renames it into the store. A version
// already in the store is swapped out only after the new one is complete,
// so ALCOM never sees a half-built folder at the registered path.
func (a *App) build(ctx context.Context, name string, rel source.Release, overrides []recipe.Edit, dir string) error {
	archive, err := a.archive(ctx, rel.ArchiveURL, rel.CacheKey, rel.Digests, rel.Pinned)
	if err != nil {
		return err
	}
	overlayArchives := make([]string, len(rel.Overlays))
	for i, o := range rel.Overlays {
		if overlayArchives[i], err = a.archive(ctx, o.ArchiveURL, o.CacheKey, fetch.Digests{}, true); err != nil {
			return fmt.Errorf("overlay %s: %w", o.Path, err)
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
	for i, o := range rel.Overlays {
		// The folder is where the archive left a submodule: it must be there
		// and empty, or the recipe names the wrong folder.
		target := filepath.Join(pkg, filepath.FromSlash(o.Path))
		entries, err := os.ReadDir(target)
		if err != nil || len(entries) > 0 {
			return fmt.Errorf("overlay %s: the package has no empty folder %s", o.Path, o.Path)
		}
		res, err := convert.ExtractTarGz(overlayArchives[i], target, "")
		if err != nil {
			return fmt.Errorf("overlay %s: %w", o.Path, err)
		}
		for _, s := range res.Skipped {
			a.logf("  skipped %s/%s (not a regular file)", o.Path, s)
		}
	}
	upstream := rel.Version.String()
	if rel.UpstreamVersion != "" {
		upstream = rel.UpstreamVersion
	}
	if err := convert.CheckManifest(pkg, name, upstream); err != nil {
		return err
	}
	if rel.UpstreamVersion != "" {
		version, err := json.Marshal(rel.Version.String())
		if err != nil {
			return err
		}
		overrides = append([]recipe.Edit{{Set: map[string]json.RawMessage{"version": version}}}, overrides...)
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

// archive returns the path of the archive kept as cacheKey, downloading it
// from url when it is not there or does not match digests. An archive
// without digests is taken only when pinned, that is, addressed by a Git
// commit.
func (a *App) archive(ctx context.Context, url, cacheKey string, digests fetch.Digests, pinned bool) (string, error) {
	archive := filepath.Join(a.Layout.Cache(), filepath.FromSlash(cacheKey))
	if fsutil.Exists(archive) {
		if err := digests.CheckFile(archive); err != nil {
			a.logf("cached archive is broken (%v); downloading again", err)
			_ = os.Remove(archive)
		}
	}
	if !fsutil.Exists(archive) {
		if digests.Empty() && !pinned {
			return "", fmt.Errorf("upstream publishes no digest for the archive")
		}
		a.logf("  downloading %s", url)
		if err := a.Client.Download(ctx, url, archive, digests); err != nil {
			return "", err
		}
	}
	return archive, nil
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

// RemoveResult is what RemoveVersions or RemoveRecipe did, or what
// RemoveRecipe would do when Applied is false.
type RemoveResult struct {
	// Applied is false when RemoveRecipe only showed what it would remove.
	Applied bool `json:"applied"`
	// Removed are the versions that are no longer available, or would not
	// be; Registered is false for each.
	Removed []Entry `json:"removed"`
	// RecipeRemoved is true when the recipe itself was removed.
	RecipeRemoved bool `json:"recipeRemoved"`
	// Registered and Unregistered are the folders whose registration with
	// ALCOM changed. Removing registers the available versions ALCOM is
	// missing, as pull does.
	Registered   []string `json:"registered"`
	Unregistered []string `json:"unregistered"`
}

// RemoveVersions unregisters and deletes available versions, and leaves the
// recipe and its version lists, so that pull can make them available again
// without an update. ref is "<id>" or "<id>/<name>": with a version it
// removes that version of every package of the recipe that has it, or of
// the named package; without one it removes every version of the named
// package, or of the recipe. A package the recipe no longer provides can
// still be removed by name. Copies already in projects stay. The result is
// nil when RemoveVersions stopped before forgetting any version.
func (a *App) RemoveVersions(ref, version string) (*RemoveResult, error) {
	id, only, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
	var res *RemoveResult
	err = a.withLock(func() error {
		st, err := state.Load(a.Layout.State())
		if err != nil {
			return err
		}
		rs, ok := st.Recipes[id]
		// Versions are removed by their record, whether the recipe still
		// provides them or not, or is gone.
		if !ok && !fsutil.Exists(a.recipePath(id)) {
			return fmt.Errorf("no recipe %q", id)
		}
		if only != "" && (!ok || rs.Packages[only] == nil || len(rs.Packages[only].Versions) == 0) {
			return fmt.Errorf("%s: no version of %s is available", id, only)
		}
		dirs, removed := takeVersions(rs, id, only, version)
		if len(dirs) == 0 {
			if version != "" {
				return fmt.Errorf("%s: version %s is not available", ref, version)
			}
			return fmt.Errorf("%s: no version is available", id)
		}
		change, saved, err := a.forget(st, dirs)
		if saved {
			res = &RemoveResult{Applied: true, Removed: removed, Registered: nonNil(change.Added), Unregistered: nonNil(change.Removed)}
		}
		return err
	})
	return res, err
}

// RemoveRecipe removes recipe id: it unregisters and deletes every
// available version, and deletes the recipe file and its version lists, so
// that using the recipe again takes an add. Without apply it only returns
// what it would remove, so that the user sees it first. A recipe whose file
// is gone can still be removed by its record, and one whose record is gone
// by its file. Copies already in projects stay. The result is nil when
// RemoveRecipe stopped before forgetting any version.
func (a *App) RemoveRecipe(id string, apply bool) (*RemoveResult, error) {
	if _, only, err := parseRef(id); err != nil || only != "" {
		return nil, fmt.Errorf("want a recipe id, not %q", id)
	}
	var res *RemoveResult
	err := a.withLock(func() error {
		st, err := state.Load(a.Layout.State())
		if err != nil {
			return err
		}
		rs, ok := st.Recipes[id]
		if !ok && !fsutil.Exists(a.recipePath(id)) {
			return fmt.Errorf("no recipe %q", id)
		}
		dirs, removed := takeVersions(rs, id, "", "")
		if !apply {
			res = &RemoveResult{Removed: removed, Registered: []string{}, Unregistered: []string{}}
			return nil
		}
		delete(st.Recipes, id)
		change, saved, err := a.forget(st, dirs)
		if saved {
			res = &RemoveResult{Applied: true, Removed: removed, Registered: nonNil(change.Added), Unregistered: nonNil(change.Removed)}
		}
		if err != nil {
			return err
		}
		for _, p := range []string{a.recipePath(id), a.Layout.Index(id)} {
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		a.logf("%s: removed recipe", id)
		res.RecipeRemoved = true
		return nil
	})
	return res, err
}

// takeVersions takes out of rs, which may be nil, the available versions of
// package only (every package when it is empty) that are version (every
// version when it is empty), and returns their folders and entries. Taking
// every version of a package forgets the package too.
func takeVersions(rs *state.RecipeState, id, only, version string) (dirs []string, removed []Entry) {
	removed = []Entry{}
	if rs == nil {
		return nil, removed
	}
	for _, name := range state.SortedKeys(rs.Packages) {
		if only != "" && name != only {
			continue
		}
		ps := rs.Packages[name]
		for _, v := range ps.SortedVersions() {
			if version != "" && v != version {
				continue
			}
			vs := ps.Versions[v]
			dirs = append(dirs, vs.Path)
			removed = append(removed, newEntry(id, name, v, vs))
			delete(ps.Versions, v)
		}
		if version == "" {
			delete(rs.Packages, name)
		}
	}
	return dirs, removed
}

// nonNil makes an empty list print as [] rather than null.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// Entry is one available version, as `vpmify package list` shows it.
type Entry struct {
	Recipe  string `json:"recipe"`
	Package string `json:"package"`
	// DisplayName is the "displayName" of the version as converted, which
	// the GUI shows with the package's name, as ALCOM does (kaikoga
	// 2026-11-19). It is empty when the version has none, or its folder
	// cannot be read.
	DisplayName string `json:"displayName,omitempty"`
	Version     string `json:"version"`
	Unity       string `json:"unity,omitempty"`
	Path        string `json:"path"`
	Registered  bool   `json:"registered"`
}

// newEntry returns the entry of available version v of package name of
// recipe id, unregistered.
func newEntry(id, name, v string, vs *state.VersionState) Entry {
	return Entry{Recipe: id, Package: name, DisplayName: readDisplayName(vs.Path), Version: v, Unity: vs.Unity, Path: vs.Path}
}

// readDisplayName returns the "displayName" in the package.json of the
// package in dir, or "" when there is none or it cannot be read: the name
// is only shown, so a broken folder is left to doctor.
func readDisplayName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var m struct {
		DisplayName string `json:"displayName"`
	}
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	return m.DisplayName
}

// ListResult is what List returns.
type ListResult struct {
	Entries []Entry `json:"entries"`
	// SettingsError says why ALCOM's settings.json could not be read; every
	// entry then shows as unregistered.
	SettingsError string `json:"settingsError,omitempty"`
}

// List returns the available versions of the recipes ids, or of every
// recipe when there are none, and whether ALCOM has each registered. The
// error is for failures that leave no list.
func (a *App) List(ids ...string) (*ListResult, error) {
	st, err := state.Load(a.Layout.State())
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		// An id that is not one is refused before it names a file, such as
		// "../state" naming state.json.
		_, ok := st.Recipes[id]
		if !ok && (!recipe.ValidID(id) || !fsutil.Exists(a.recipePath(id))) {
			return nil, fmt.Errorf("no recipe %q", id)
		}
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
		if len(ids) > 0 && !slices.Contains(ids, id) {
			continue
		}
		rs := st.Recipes[id]
		for _, name := range state.SortedKeys(rs.Packages) {
			ps := rs.Packages[name]
			for _, v := range ps.SortedVersions() {
				vs := ps.Versions[v]
				e := newEntry(id, name, v, vs)
				e.Registered = registered[filepath.Clean(vs.Path)]
				res.Entries = append(res.Entries, e)
			}
		}
	}
	return res, nil
}

// RecipeInfo is one of the user's recipes.
type RecipeInfo struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName,omitempty"`
	Packages    []string `json:"packages"`
	// Origin is the file path or URL the recipe was added from.
	Origin string `json:"origin,omitempty"`
	// Problem says why the recipe or its version lists cannot be used, such
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
		info := RecipeInfo{ID: id, Packages: []string{}}
		if rs, ok := st.Recipes[id]; ok {
			info.Origin = rs.Origin
		}
		r, err := a.loadRecipe(id)
		if err == nil {
			info.DisplayName = r.DisplayName
			info.Packages = r.Names()
			_, err = a.loadLists(r)
		}
		if err != nil {
			info.Problem = err.Error()
		}
		out = append(out, info)
	}
	return out, nil
}

// VersionInfo is one version in a package's version list, as `vpmify
// recipe versions` shows it.
type VersionInfo struct {
	Version string `json:"version"`
	// Unity is the "unity" field after the recipe's overrides.
	Unity      string `json:"unity,omitempty"`
	Origin     string `json:"origin"`
	Prerelease bool   `json:"prerelease,omitempty"`
	// Available means Vpmify has converted it and registered it with ALCOM.
	Available bool `json:"available"`
	// Unusable says why the version cannot be converted, such as {sameTag}
	// naming a package whose version in the same tag is not in its list.
	Unusable string `json:"unusable,omitempty"`
}

// VersionList is what `vpmify recipe versions` shows for one recipe.
type VersionList struct {
	Recipe   string            `json:"recipe"`
	Packages []PackageVersions `json:"packages"`
}

// PackageVersions is the version list of one package.
type PackageVersions struct {
	Package string `json:"package"`
	// DisplayName is the "displayName" of the newest available version that
	// has one. The version lists do not keep it, so it is empty while no
	// version is available.
	DisplayName string `json:"displayName,omitempty"`
	Source      string `json:"source"`
	// Updated is when the version list was read from upstream.
	Updated  time.Time     `json:"updated"`
	Versions []VersionInfo `json:"versions"`
}

// VersionsOptions narrow what Versions lists. With none set it lists every
// stable version in the version list (kaikoga 2026-11-18).
type VersionsOptions struct {
	// All lists prereleases too.
	All bool
	// Unity lists only the versions a project on this Unity can use, read
	// the way vrc-get reads "unity": as the oldest Unity the version
	// supports (vrc-get-vpm/src/lib.rs, unity_compatible). It is a Unity
	// version such as "2022.3" or "2022.3.22f1".
	Unity string
}

// Versions lists the versions in the version lists of ref, "<id>" or
// "<id>/<name>", newest first, package by package. It does not go to the
// network. When some of the recipe's sources have no usable list, it lists
// the others and returns the error too.
func (a *App) Versions(ref string, opt VersionsOptions) (*VersionList, error) {
	id, only, err := parseRef(ref)
	if err != nil {
		return nil, err
	}
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
	if only != "" {
		if src, _ := r.Package(only); src == nil {
			return nil, fmt.Errorf("recipe %q has no package %s", id, only)
		}
	}
	lists, listErr := a.loadLists(r)
	if only != "" {
		if l := find(lists, only); l != nil {
			lists, listErr = []*pkgList{l}, nil
		} else {
			lists = nil
		}
	}
	// The packages whose source has a list are shown with the error about
	// the others, as pull goes on with them.
	if len(lists) == 0 && listErr != nil {
		return nil, listErr
	}
	st, err := state.Load(a.Layout.State())
	if err != nil {
		return nil, err
	}
	out := &VersionList{Recipe: id, Packages: []PackageVersions{}}
	for _, l := range lists {
		name := l.pkg.Name
		available := st.AvailableVersions(id, name)
		pv := PackageVersions{Package: name, Source: l.src.Describe(), Updated: l.updated, Versions: []VersionInfo{}}
		for i := len(l.releases) - 1; i >= 0; i-- {
			rel := l.releases[i]
			v := rel.Version.String()
			_, isAvailable := available[v]
			unity := l.pkg.Unity(rel.Version, rel.Unity)
			// The name comes from any available version, shown or not.
			if isAvailable && pv.DisplayName == "" {
				pv.DisplayName = readDisplayName(available[v].Path)
			}
			switch {
			case rel.Version.IsPrerelease() && !opt.All:
				continue
			case opt.Unity != "" && !project.canUse(unity):
				continue
			}
			info := VersionInfo{
				Version:    v,
				Unity:      unity,
				Origin:     rel.Origin,
				Prerelease: rel.Version.IsPrerelease(),
				Available:  isAvailable,
			}
			if _, err := l.expand(rel); err != nil {
				info.Unusable = err.Error()
			}
			pv.Versions = append(pv.Versions, info)
		}
		out.Packages = append(out.Packages, pv)
	}
	return out, listErr
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
