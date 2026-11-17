// Package index keeps the version list `vpmify update` reads from upstream
// for one recipe: what each version is and where its archive is. `vpmify
// pull` and `vpmify versions` read only this list, so what they show and
// place does not change until the next update (kaikoga 2026-11-18).
package index

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/fsutil"
	"github.com/kaikoga/vpmify/internal/semver"
	"github.com/kaikoga/vpmify/internal/source"
)

// Format is the version of the index file layout.
const Format = 1

// ErrNoIndex means the recipe has not been updated yet.
var ErrNoIndex = errors.New("no version list")

// Index is the version list of one recipe.
type Index struct {
	Format int `json:"format"`
	// Key is the recipe's ListingKey when the list was read. A recipe whose
	// key differs now needs another update.
	Key      string    `json:"key"`
	Updated  time.Time `json:"updated"`
	Releases []Entry   `json:"releases"`
}

// Entry is one version upstream offers, as source.Release has it.
type Entry struct {
	Version      string            `json:"version"`
	Unity        string            `json:"unity,omitempty"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
	Dir          string            `json:"dir,omitempty"`
	ArchiveURL   string            `json:"archiveUrl"`
	Integrity    string            `json:"integrity,omitempty"`
	Shasum       string            `json:"shasum,omitempty"`
	Pinned       bool              `json:"pinned,omitempty"`
	CacheKey     string            `json:"cacheKey"`
	Origin       string            `json:"origin"`
}

// New makes an index of releases, read now for a recipe with key.
func New(key string, releases []source.Release, now time.Time) *Index {
	idx := &Index{Format: Format, Key: key, Updated: now.UTC(), Releases: []Entry{}}
	for _, r := range releases {
		idx.Releases = append(idx.Releases, Entry{
			Version:      r.Version.String(),
			Unity:        r.Unity,
			Dependencies: r.Dependencies,
			Dir:          r.Dir,
			ArchiveURL:   r.ArchiveURL,
			Integrity:    r.Digests.Integrity,
			Shasum:       r.Digests.Shasum,
			Pinned:       r.Pinned,
			CacheKey:     r.CacheKey,
			Origin:       r.Origin,
		})
	}
	return idx
}

// Load reads an index file. A missing file is ErrNoIndex.
func Load(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoIndex
	}
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if idx.Format != Format {
		return nil, fmt.Errorf("%s: unknown format %d", path, idx.Format)
	}
	return &idx, nil
}

// Save writes the index file.
func (idx *Index) Save(path string) error {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(data, '\n'), 0o644)
}

// SourceReleases returns the versions as source releases, oldest first.
func (idx *Index) SourceReleases() ([]source.Release, error) {
	out := make([]source.Release, 0, len(idx.Releases))
	for _, e := range idx.Releases {
		v, err := semver.Parse(e.Version)
		if err != nil {
			return nil, err
		}
		out = append(out, source.Release{
			Version:      v,
			Unity:        e.Unity,
			Dependencies: e.Dependencies,
			Dir:          e.Dir,
			ArchiveURL:   e.ArchiveURL,
			Digests:      fetch.Digests{Integrity: e.Integrity, Shasum: e.Shasum},
			Pinned:       e.Pinned,
			CacheKey:     e.CacheKey,
			Origin:       e.Origin,
		})
	}
	return out, nil
}
