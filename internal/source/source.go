// Package source lists the versions an upstream offers and fetches the
// archive of each.
package source

import (
	"context"
	"fmt"

	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/recipe"
	"github.com/kaikoga/vpmify/internal/semver"
)

// Release is one version an upstream offers.
type Release struct {
	Version semver.Version
	// Unity is the upstream "unity" field of the version, before overrides.
	Unity string
	// ArchiveURL and Digests locate the archive of the version.
	ArchiveURL string
	Digests    fetch.Digests
}

// Source is an upstream.
type Source interface {
	// Name is the package name the source publishes.
	Name() string
	// Releases lists every version the upstream offers.
	Releases(ctx context.Context) ([]Release, error)
	// Format is the archive format of the releases ("tgz").
	Format() string
	// CacheKey is a file name, unique within the source, for the archive of r.
	CacheKey(r Release) string
}

// New returns the source a recipe names.
func New(src recipe.Source, client *fetch.Client) (Source, error) {
	switch src.Type {
	case recipe.SourceNPM:
		return &NPM{Registry: src.Registry, Package: src.Name, Client: client}, nil
	default:
		return nil, fmt.Errorf("source type %q is not supported", src.Type)
	}
}
