// Package source lists the versions an upstream offers and where the
// archive of each is.
package source

import (
	"context"
	"fmt"

	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/recipe"
	"github.com/kaikoga/vpmify/internal/semver"
)

// Release is one version of a package an upstream offers.
type Release struct {
	Version semver.Version
	// UpstreamVersion is the version the upstream package.json has, when
	// Vpmify gives the package another (a recipe's versionFromTag). Empty
	// means package.json has Version.
	UpstreamVersion string
	// Unity is the upstream "unity" field of the version, before overrides.
	Unity string
	// Dependencies are the upstream UPM "dependencies" of the version.
	Dependencies map[string]string
	// SameTag are the versions the source's other packages have in the tag
	// this version was taken from (Git sources), for {sameTag}.
	SameTag map[string]string
	// Dir is the folder of the package inside the archive, after the
	// archive's top folder is dropped. Empty means the top.
	Dir string
	// ArchiveURL and Digests locate the archive of the version.
	ArchiveURL string
	Digests    fetch.Digests
	// Pinned means the URL names fixed content (a Git commit), so the
	// archive is accepted without a published digest.
	Pinned bool
	// CacheKey is where the archive is kept, relative to the cache folder.
	// Releases that share an archive share the key.
	CacheKey string
	// Origin says where the version was found, for messages.
	Origin string
	// Tag is the Git tag the version was taken from (Git sources). The
	// version lists do not keep it: only probing the source reads it.
	Tag string
	// Overlays go into folders of the package, in the recipe's order.
	Overlays []Overlay
}

// Overlay is the content of another archive that goes into a folder of the
// package. The archive is a Git commit's, so it needs no digest.
type Overlay struct {
	// Path is the folder, relative to the package's folder.
	Path string
	// ArchiveURL and CacheKey are as in Release.
	ArchiveURL string
	CacheKey   string
}

// Source is an upstream of the packages of one recipe source.
type Source interface {
	// Releases lists the versions the upstream offers for each package, by
	// package name, oldest first, and warnings for whoever keeps the recipe
	// (Git tags that read as one version). It fails as a whole: a source's
	// versions are kept together, so that {sameTag} sees one reading of the
	// tags.
	Releases(ctx context.Context) (map[string][]Release, []string, error)
}

// Skip is a tag or version an upstream has that a source does not take,
// and why, for whoever writes the recipe: the versions a user's update
// reads leave these out without a word, since the user cannot mend them.
type Skip struct {
	// Tag is the Git tag, if the source has tags.
	Tag string `json:"tag,omitempty"`
	// Package is the package the tag or version is left out for, or empty
	// for every package of the source.
	Package string `json:"package,omitempty"`
	// Version is what the tag or version reads as, if it reads as one.
	Version string `json:"version,omitempty"`
	Reason  string `json:"reason"`
}

// Prober is a Source that says what it leaves out.
type Prober interface {
	Source
	// Probe is Releases, with what it does not take and why.
	Probe(ctx context.Context) (map[string][]Release, []Skip, []string, error)
}

// New returns the upstream of a recipe source. cacheDir keeps what the
// source reads per version and never changes (a Git commit's package.json).
func New(s *recipe.Source, client *fetch.Client, cacheDir string) (Source, error) {
	switch s.Type {
	case recipe.SourceNPM:
		return &NPM{Registry: s.Registry, Packages: s.Names(), Client: client}, nil
	case recipe.SourceGitHub:
		return NewGitHub(s, client, cacheDir)
	default:
		return nil, fmt.Errorf("source type %q is not supported", s.Type)
	}
}
