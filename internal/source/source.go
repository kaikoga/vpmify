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

// Release is one version of the package an upstream offers.
type Release struct {
	Version semver.Version
	// Unity is the upstream "unity" field of the version, before overrides.
	Unity string
	// Dependencies are the upstream UPM "dependencies" of the version.
	Dependencies map[string]string
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
}

// Source is an upstream of one package.
type Source interface {
	// Releases lists the versions the upstream offers, oldest first.
	Releases(ctx context.Context) ([]Release, error)
}

// New returns the source a recipe names. cacheDir keeps what the source
// reads per version and never changes (a Git commit's package.json).
func New(r *recipe.Recipe, client *fetch.Client, cacheDir string) (Source, error) {
	switch r.Source.Type {
	case recipe.SourceNPM:
		return &NPM{Registry: r.Source.Registry, Package: r.Source.Name, Client: client}, nil
	case recipe.SourceGit:
		return NewGitHub(r, client, cacheDir)
	default:
		return nil, fmt.Errorf("source type %q is not supported", r.Source.Type)
	}
}
