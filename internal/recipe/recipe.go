// Package recipe reads recipes: the user's definition of what to convert,
// where it comes from, and how to fix its package.json.
package recipe

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"github.com/kaikoga/vpmify/internal/semver"
)

// Select modes.
const (
	// SelectLatest adds the newest version for each value of the "unity"
	// field on every sync. Versions placed before are kept.
	SelectLatest = "latest"
	// SelectAll adds every version in range.
	SelectAll = "all"
)

// Source types.
const (
	SourceNPM = "npm"
	SourceGit = "git"
)

// Recipe describes one upstream source.
type Recipe struct {
	ID         string     `json:"id"`
	Source     Source     `json:"source"`
	Path       string     `json:"path,omitempty"`
	Select     string     `json:"select,omitempty"`
	Versions   string     `json:"versions,omitempty"`
	Prerelease bool       `json:"prerelease,omitempty"`
	Overrides  []Override `json:"overrides,omitempty"`

	versions  semver.Range
	overrides []parsedOverride
}

// Source says where the package comes from.
type Source struct {
	Type string `json:"type"`
	// Registry and Name are for npm-compatible registries.
	Registry string `json:"registry,omitempty"`
	Name     string `json:"name,omitempty"`
	// URL and TagPattern are for Git repositories.
	URL        string `json:"url,omitempty"`
	TagPattern string `json:"tagPattern,omitempty"`
}

// Override sets top-level fields of package.json for the versions in range.
// A null value removes the field.
type Override struct {
	Versions string                     `json:"versions,omitempty"`
	Set      map[string]json.RawMessage `json:"set"`
}

type parsedOverride struct {
	versions semver.Range
	set      map[string]json.RawMessage
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Fields that decide where a version lives in the store and how ALCOM
// identifies it, so a recipe must not change them.
var protectedFields = map[string]bool{"name": true, "version": true}

// Load reads a recipe file.
func Load(path string) (*Recipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// Parse reads and checks a recipe.
func Parse(data []byte) (*Recipe, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r Recipe
	if err := dec.Decode(&r); err != nil {
		return nil, err
	}
	if err := r.check(); err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *Recipe) check() error {
	if !idPattern.MatchString(r.ID) {
		return fmt.Errorf("id %q must be lowercase letters, digits, '.', '_' or '-'", r.ID)
	}
	switch r.Source.Type {
	case SourceNPM:
		if r.Source.Registry == "" || r.Source.Name == "" {
			return fmt.Errorf("npm source needs registry and name")
		}
	case SourceGit:
		return fmt.Errorf("git source is not supported yet")
	default:
		return fmt.Errorf("unknown source type %q", r.Source.Type)
	}
	switch r.Select {
	case "":
		r.Select = SelectLatest
	case SelectLatest, SelectAll:
	default:
		return fmt.Errorf("unknown select %q (want %q or %q)", r.Select, SelectLatest, SelectAll)
	}
	rng, err := semver.ParseRange(r.Versions)
	if err != nil {
		return fmt.Errorf("versions: %w", err)
	}
	r.versions = rng
	for i, o := range r.Overrides {
		rng, err := semver.ParseRange(o.Versions)
		if err != nil {
			return fmt.Errorf("overrides[%d].versions: %w", i, err)
		}
		if len(o.Set) == 0 {
			return fmt.Errorf("overrides[%d].set is empty", i)
		}
		for k := range o.Set {
			if protectedFields[k] {
				return fmt.Errorf("overrides[%d] must not set %q", i, k)
			}
		}
		r.overrides = append(r.overrides, parsedOverride{versions: rng, set: o.Set})
	}
	return nil
}

// Accepts reports whether the recipe takes version v at all.
func (r *Recipe) Accepts(v semver.Version) bool {
	if v.IsPrerelease() && !r.Prerelease {
		return false
	}
	return r.versions.Contains(v)
}

// OverridesFor returns the field sets that apply to v, in recipe order.
func (r *Recipe) OverridesFor(v semver.Version) []map[string]json.RawMessage {
	var out []map[string]json.RawMessage
	for _, o := range r.overrides {
		if o.versions.Contains(v) {
			out = append(out, o.set)
		}
	}
	return out
}

// convertRevision changes when Vpmify starts to convert differently, so that
// versions placed by an older Vpmify are built again.
const convertRevision = 1

// Fingerprint identifies how version v is built from upstream. A placed
// version whose fingerprint differs is built again.
func (r *Recipe) Fingerprint(v semver.Version) string {
	h := sha256.New()
	// encoding/json writes map keys sorted, so this is stable.
	data, _ := json.Marshal(struct {
		Revision  int                          `json:"revision"`
		Source    Source                       `json:"source"`
		Path      string                       `json:"path"`
		Overrides []map[string]json.RawMessage `json:"overrides"`
	}{convertRevision, r.Source, r.Path, r.OverridesFor(v)})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Unity returns the "unity" field of version v after overrides, given the
// upstream value.
func (r *Recipe) Unity(v semver.Version, upstream string) string {
	unity := upstream
	for _, set := range r.OverridesFor(v) {
		raw, ok := set["unity"]
		if !ok {
			continue
		}
		var s *string
		if err := json.Unmarshal(raw, &s); err != nil || s == nil {
			unity = ""
			continue
		}
		unity = *s
	}
	return unity
}
