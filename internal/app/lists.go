package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kaikoga/vpmify/internal/index"
	"github.com/kaikoga/vpmify/internal/recipe"
	"github.com/kaikoga/vpmify/internal/semver"
	"github.com/kaikoga/vpmify/internal/source"
)

// pkgList is one package of a recipe, with the versions its source's
// version list holds.
type pkgList struct {
	src     *recipe.Source
	pkg     *recipe.Package
	updated time.Time
	// releases are the versions in the package's range, oldest first.
	releases  []source.Release
	byVersion map[string]source.Release
	// listed are the versions the list holds for each package of the
	// source, which {sameTag} may name.
	listed map[string]map[string]bool
}

// errNoList means a source of the recipe has no version list that can be
// used, which only an update mends.
var errNoList = errors.New("no usable version list")

// loadLists returns the packages of recipe r whose source has a version
// list for what the source is now. The error names the sources that have
// none; the lists of the others are returned with it.
func (a *App) loadLists(r *recipe.Recipe) ([]*pkgList, error) {
	idx, err := index.Load(a.Layout.Index(r.ID))
	if errors.Is(err, index.ErrNoIndex) {
		return nil, fmt.Errorf("%w: none yet; run `vpmify recipe update %s`", errNoList, r.ID)
	}
	if err != nil {
		// update writes the list anew whatever is there, so it mends this.
		return nil, fmt.Errorf("%w: it cannot be read (%v); run `vpmify recipe update %s`", errNoList, err, r.ID)
	}
	var out []*pkgList
	var errs []error
	for _, s := range r.Sources {
		l := idx.List(s.ListingKey())
		if l == nil {
			// The source may list other folders or come from elsewhere now,
			// or the last update could not read it.
			errs = append(errs, fmt.Errorf("%w for %s: the recipe changed since it was read, or the last update could not read it; run `vpmify recipe update %s`", errNoList, s.Describe(), r.ID))
			continue
		}
		listed := map[string]map[string]bool{}
		var pkgs []*pkgList
		for _, p := range s.Packages {
			releases, err := l.Releases(p.Name)
			if err != nil {
				return nil, fmt.Errorf("%w: %v; run `vpmify recipe update %s`", errNoList, err, r.ID)
			}
			pl := &pkgList{src: s, pkg: p, updated: l.Updated, releases: releases, byVersion: map[string]source.Release{}, listed: listed}
			listed[p.Name] = map[string]bool{}
			for _, rel := range releases {
				pl.byVersion[rel.Version.String()] = rel
				listed[p.Name][rel.Version.String()] = true
			}
			pkgs = append(pkgs, pl)
		}
		out = append(out, pkgs...)
	}
	return out, errors.Join(errs...)
}

// find returns the list of package name, or nil.
func find(lists []*pkgList, name string) *pkgList {
	for _, l := range lists {
		if l.pkg.Name == name {
			return l
		}
	}
	return nil
}

// expand returns the overrides of rel with their placeholders filled in.
// {sameTag} takes a version only when the version list holds it, so that a
// version is converted only when what it depends on in its tag can be made
// available too; an error means the version cannot be converted.
func (l *pkgList) expand(rel source.Release) ([]map[string]json.RawMessage, error) {
	sameTag := map[string]string{}
	for name, v := range rel.SameTag {
		if l.listed[name][v] {
			sameTag[name] = v
		}
	}
	return l.pkg.ExpandedOverridesFor(rel.Version, rel.Dependencies, sameTag)
}

// usable reports whether rel can be converted.
func (l *pkgList) usable(rel source.Release) bool {
	_, err := l.expand(rel)
	return err == nil
}

// fingerprint returns the fingerprint of rel built with the overrides
// expanded for it.
func (l *pkgList) fingerprint(rel source.Release, expanded []map[string]json.RawMessage) string {
	return l.src.Fingerprint(l.pkg, rel.Dir, rel.CacheKey, expanded)
}

// accepted returns the releases pull chooses from without their being
// named, in order: those the package accepts and that can be converted.
func (l *pkgList) accepted() []source.Release {
	var out []source.Release
	for _, rel := range l.releases {
		if l.pkg.Accepts(rel.Version) && l.usable(rel) {
			out = append(out, rel)
		}
	}
	return out
}

// newest returns the newest of the accepted releases, which must not be
// empty: the one version pull makes available without being told, whatever
// Unity it needs (kaikoga 2026-11-19). Before, pull took the newest for each
// value of "unity", which also built versions for Unity versions nobody had.
// A project on an older Unity sees the newest among the versions ALCOM marks
// incompatible, and the user names an older version to pull.
func newest(accepted []source.Release) source.Release {
	out := accepted[0]
	for _, rel := range accepted[1:] {
		if semver.Compare(rel.Version, out.Version) > 0 {
			out = rel
		}
	}
	return out
}
