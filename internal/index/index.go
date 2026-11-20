// Package index keeps the version lists `vpmify recipe update` reads from
// upstream for one recipe: what each version is and where its archive is.
// `vpmify package pull` and `vpmify recipe versions` read only these lists,
// so what they show and make available does not change until the next
// update (kaikoga 2026-11-18).
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

// Format is the version of the index file layout. 2: one list per source.
const Format = 2

// ErrNoIndex means the recipe has not been updated yet.
var ErrNoIndex = errors.New("no version list")

// Index is the version lists of one recipe, one per source. A source's list
// is read as a whole, and the lists of a recipe's sources are not: one
// upstream that fails leaves the other sources' lists new (kaikoga
// 2026-11-19).
type Index struct {
	Format  int     `json:"format"`
	Sources []*List `json:"sources"`
}

// List is the version list of one source.
type List struct {
	// Key is the source's ListingKey when the list was read. A source whose
	// key differs now needs another update.
	Key     string    `json:"key"`
	Updated time.Time `json:"updated"`
	// Packages are the versions of each package, oldest first.
	Packages map[string][]Entry `json:"packages"`
}

// Entry is one version upstream offers, as source.Release has it.
type Entry struct {
	Version         string            `json:"version"`
	UpstreamVersion string            `json:"upstreamVersion,omitempty"`
	Unity           string            `json:"unity,omitempty"`
	Dependencies    map[string]string `json:"dependencies,omitempty"`
	SameTag         map[string]string `json:"sameTag,omitempty"`
	Dir             string            `json:"dir,omitempty"`
	ArchiveURL      string            `json:"archiveUrl"`
	Integrity       string            `json:"integrity,omitempty"`
	Shasum          string            `json:"shasum,omitempty"`
	Pinned          bool              `json:"pinned,omitempty"`
	CacheKey        string            `json:"cacheKey"`
	Origin          string            `json:"origin"`
	Overlays        []Overlay         `json:"overlays,omitempty"`
}

// Overlay is one overlay of an entry, as source.Overlay has it.
type Overlay struct {
	Path       string `json:"path"`
	ArchiveURL string `json:"archiveUrl"`
	CacheKey   string `json:"cacheKey"`
}

// NewList makes the list of releases, by package name, read now for a
// source with key.
func NewList(key string, releases map[string][]source.Release, now time.Time) *List {
	l := &List{Key: key, Updated: now.UTC(), Packages: map[string][]Entry{}}
	for name, rels := range releases {
		entries := []Entry{}
		for _, r := range rels {
			var overlays []Overlay
			for _, o := range r.Overlays {
				overlays = append(overlays, Overlay{Path: o.Path, ArchiveURL: o.ArchiveURL, CacheKey: o.CacheKey})
			}
			entries = append(entries, Entry{
				Version:         r.Version.String(),
				UpstreamVersion: r.UpstreamVersion,
				Unity:           r.Unity,
				Dependencies:    r.Dependencies,
				SameTag:         r.SameTag,
				Dir:             r.Dir,
				ArchiveURL:      r.ArchiveURL,
				Integrity:       r.Digests.Integrity,
				Shasum:          r.Digests.Shasum,
				Pinned:          r.Pinned,
				CacheKey:        r.CacheKey,
				Origin:          r.Origin,
				Overlays:        overlays,
			})
		}
		l.Packages[name] = entries
	}
	return l
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
	idx.Format = Format
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(data, '\n'), 0o644)
}

// List returns the list read for a source with key, or nil.
func (idx *Index) List(key string) *List {
	if idx == nil {
		return nil
	}
	for _, l := range idx.Sources {
		if l != nil && l.Key == key {
			return l
		}
	}
	return nil
}

// Releases returns the versions of package name as source releases, oldest
// first.
func (l *List) Releases(name string) ([]source.Release, error) {
	entries := l.Packages[name]
	out := make([]source.Release, 0, len(entries))
	for _, e := range entries {
		v, err := semver.Parse(e.Version)
		if err != nil {
			return nil, err
		}
		var overlays []source.Overlay
		for _, o := range e.Overlays {
			overlays = append(overlays, source.Overlay{Path: o.Path, ArchiveURL: o.ArchiveURL, CacheKey: o.CacheKey})
		}
		out = append(out, source.Release{
			Version:         v,
			UpstreamVersion: e.UpstreamVersion,
			Unity:           e.Unity,
			Dependencies:    e.Dependencies,
			SameTag:         e.SameTag,
			Dir:             e.Dir,
			ArchiveURL:      e.ArchiveURL,
			Digests:         fetch.Digests{Integrity: e.Integrity, Shasum: e.Shasum},
			Pinned:          e.Pinned,
			CacheKey:        e.CacheKey,
			Origin:          e.Origin,
			Overlays:        overlays,
		})
	}
	return out, nil
}
