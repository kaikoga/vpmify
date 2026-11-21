// Package state keeps what Vpmify cannot read from the store: where each
// recipe was added from, and how each version in the store was built. Which
// versions are available is what ALCOM's settings.json lists, and where a
// version is and what its package.json says are read from the store.
// It lives in Vpmify's own folder, not in ALCOM's settings.json, because the
// official VCC may not keep fields it does not know.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"

	"github.com/kaikoga/vpmify/internal/fsutil"
	"github.com/kaikoga/vpmify/internal/semver"
)

// Format is the version of the state file layout. Format 1 also kept each
// version's folder and "unity", which Load drops: the folder follows from
// the store, and "unity" is in the version's package.json.
const Format = 2

// State is the whole record.
type State struct {
	Format  int                     `json:"format"`
	Recipes map[string]*RecipeState `json:"recipes"`
}

// RecipeState is what Vpmify keeps for one recipe.
type RecipeState struct {
	// Origin is where the recipe was added from (a file path or URL).
	Origin   string                   `json:"origin,omitempty"`
	Packages map[string]*PackageState `json:"packages"`
}

// PackageState is the versions of one package name in the store.
type PackageState struct {
	Versions map[string]*VersionState `json:"versions"`
}

// VersionState is one version in the store.
type VersionState struct {
	// Fingerprint says how the version was built, so that it is built
	// again when the recipe now builds it differently.
	Fingerprint string `json:"fingerprint"`
}

// New returns an empty state.
func New() *State {
	return &State{Format: Format, Recipes: map[string]*RecipeState{}}
}

// Load reads the state file. A missing file is an empty state.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	s := New()
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// encoding/json leaves out the fields of format 1 that are gone.
	if s.Format != Format && s.Format != 1 {
		return nil, fmt.Errorf("%s: unknown format %d", path, s.Format)
	}
	s.Format = Format
	if s.Recipes == nil {
		s.Recipes = map[string]*RecipeState{}
	}
	return s, nil
}

// Save writes the state file.
func (s *State) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(data, '\n'), 0o644)
}

// Recipe returns the record of recipe id, creating it.
func (s *State) Recipe(id string) *RecipeState {
	r, ok := s.Recipes[id]
	if !ok {
		r = &RecipeState{}
		s.Recipes[id] = r
	}
	if r.Packages == nil {
		r.Packages = map[string]*PackageState{}
	}
	return r
}

// Package returns the record of package name, creating it.
func (r *RecipeState) Package(name string) *PackageState {
	p, ok := r.Packages[name]
	if !ok {
		p = &PackageState{}
		r.Packages[name] = p
	}
	if p.Versions == nil {
		p.Versions = map[string]*VersionState{}
	}
	return p
}

// StoredVersions returns the versions of package name that recipe id has
// in the store, without creating records.
func (s *State) StoredVersions(id, name string) map[string]*VersionState {
	r, ok := s.Recipes[id]
	if !ok {
		return nil
	}
	p, ok := r.Packages[name]
	if !ok {
		return nil
	}
	return p.Versions
}

// SortedVersions returns the version keys of p, oldest first. Keys that are
// not SemVer come first, in string order.
func (p *PackageState) SortedVersions() []string {
	keys := make([]string, 0, len(p.Versions))
	for k := range p.Versions {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return VersionLess(keys[i], keys[j]) })
	return keys
}

// VersionLess orders version keys the way SortedVersions does.
func VersionLess(x, y string) bool {
	a, aerr := semver.Parse(x)
	b, berr := semver.Parse(y)
	if aerr != nil || berr != nil {
		if aerr != nil && berr != nil {
			return x < y
		}
		return aerr != nil
	}
	return semver.Compare(a, b) < 0
}

// SortedKeys returns the keys of a map, sorted.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
