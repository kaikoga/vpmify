package app

import (
	"context"
	"fmt"

	"github.com/kaikoga/vpmify/internal/fsutil"
	"github.com/kaikoga/vpmify/internal/recipe"
	"github.com/kaikoga/vpmify/internal/source"
)

// ProbeResult is how a recipe reads its upstreams now, for whoever writes
// the recipe. Upstreams change, so probing again may read otherwise.
type ProbeResult struct {
	Recipe  string        `json:"recipe"`
	Sources []ProbeSource `json:"sources"`
}

// ProbeSource is how one source of the recipe reads its upstream.
type ProbeSource struct {
	Source string `json:"source"`
	// Packages are the versions the source takes, oldest first: what an
	// update would keep in the version list.
	Packages []ProbePackage `json:"packages"`
	// Skipped are the tags and versions the upstream has that the source
	// does not take, and why. An update leaves them out without a word.
	Skipped []source.Skip `json:"skipped"`
	// Warnings are what an update warns of (Git tags that read as one
	// version).
	Warnings []string `json:"warnings"`
	// Error is why the upstream could not be read, with nothing else.
	Error string `json:"error,omitempty"`
}

// ProbePackage is the versions a source takes for one package.
type ProbePackage struct {
	Package  string         `json:"package"`
	Versions []ProbeVersion `json:"versions"`
}

// ProbeVersion is one version a source takes, and the Git tag it is taken
// from, if any.
type ProbeVersion struct {
	Version string `json:"version"`
	Tag     string `json:"tag,omitempty"`
}

// Probe reads the upstreams of the recipe ref names, the id of one of the
// user's recipes, or else a recipe file or https URL as add takes, and
// says which versions each source takes and which it leaves out and why.
// It writes no recipe, version list or state, only the download cache. A
// source that cannot be read says so in its ProbeSource, and the others go
// on.
func (a *App) Probe(ctx context.Context, ref string) (*ProbeResult, error) {
	var r *recipe.Recipe
	if recipe.ValidID(ref) && fsutil.Exists(a.recipePath(ref)) {
		var err error
		if r, err = a.loadRecipe(ref); err != nil {
			return nil, err
		}
	} else {
		d, err := a.readRecipeRef(ctx, ref)
		if err != nil {
			return nil, err
		}
		r = d.recipe
	}
	res := &ProbeResult{Recipe: r.ID, Sources: []ProbeSource{}}
	for _, s := range r.Sources {
		if ctx.Err() != nil {
			return res, context.Cause(ctx)
		}
		res.Sources = append(res.Sources, a.probeSource(ctx, s))
	}
	return res, nil
}

func (a *App) probeSource(ctx context.Context, s *recipe.Source) ProbeSource {
	ps := ProbeSource{Source: s.Describe(), Packages: []ProbePackage{}, Skipped: []source.Skip{}, Warnings: []string{}}
	src, err := source.New(s, a.Client, a.Layout.Cache())
	if err != nil {
		ps.Error = err.Error()
		return ps
	}
	p, ok := src.(source.Prober)
	if !ok {
		ps.Error = fmt.Sprintf("source type %q cannot be probed", s.Type)
		return ps
	}
	upstream, skips, warnings, err := p.Probe(ctx)
	if err != nil {
		ps.Error = err.Error()
		return ps
	}
	ps.Skipped = append(ps.Skipped, skips...)
	ps.Warnings = append(ps.Warnings, warnings...)
	for _, pkg := range s.Packages {
		pp := ProbePackage{Package: pkg.Name, Versions: []ProbeVersion{}}
		for _, rel := range upstream[pkg.Name] {
			// As readSource keeps only the versions in the package's range.
			if !pkg.InRange(rel.Version) {
				ps.Skipped = append(ps.Skipped, source.Skip{Tag: rel.Tag, Package: pkg.Name, Version: rel.Version.String(), Reason: "outside the package's versions"})
				continue
			}
			pp.Versions = append(pp.Versions, ProbeVersion{Version: rel.Version.String(), Tag: rel.Tag})
		}
		ps.Packages = append(ps.Packages, pp)
	}
	return ps
}
