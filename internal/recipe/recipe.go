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
	"path"
	"regexp"
	"strings"

	"github.com/kaikoga/vpmify/internal/semver"
)

// Source types.
const (
	SourceNPM = "npm"
	// SourceGitHub is a repository on github.com. It is not a plain Git
	// source: it reads files through GitHub's own endpoints. A plain Git
	// source would be another type (kaikoga 2026-11-18).
	SourceGitHub = "github"
)

// Recipe describes one package, where it comes from and how to convert it.
// Which versions to make available is not part of it: the user chooses that with
// each command (kaikoga 2026-11-18).
type Recipe struct {
	ID     string `json:"id"`
	Source Source `json:"source"`
	// Path is the folder of the package inside a Git repository, for every
	// tag. Empty means the repository root.
	Path string `json:"path,omitempty"`
	// Paths give the folder by tag instead, for a package that moved (UniVRM
	// moved Assets/VRM to Packages/VRM in v0.131.0). The ranges are on the
	// tag's version, not the package's: the package's version is in the
	// package.json that the folder holds. A tag that no range takes is not
	// read.
	Paths []PathRule `json:"paths,omitempty"`
	// Versions and Prerelease say which versions the recipe can convert.
	Versions   string     `json:"versions,omitempty"`
	Prerelease bool       `json:"prerelease,omitempty"`
	Overrides  []Override `json:"overrides,omitempty"`

	versions  semver.Range
	tags      semver.Range
	paths     []parsedPathRule
	overrides []parsedOverride
}

// PathRule is the folder of the package for the tags in range. Path is a
// pointer so that a rule without "path" is refused instead of reading the
// repository root, which "" means.
type PathRule struct {
	Tags string  `json:"tags,omitempty"`
	Path *string `json:"path"`
}

type parsedPathRule struct {
	tags semver.Range
	path string
}

// Source says where the package comes from.
type Source struct {
	Type string `json:"type"`
	// Name is the package name, as package.json writes it.
	Name string `json:"name"`
	// Registry is for npm-compatible registries.
	Registry string `json:"registry,omitempty"`
	// URL, TagPattern and Tags are for Git repositories. TagPattern is a
	// glob on tag names (default "*"). Tags is a range on the version in the
	// tag name (a leading "v" dropped), which limits the tags Vpmify reads;
	// it differs from Recipe.Versions, the range on the package's version,
	// because a repository's tags need not follow its packages' versions.
	URL        string `json:"url,omitempty"`
	TagPattern string `json:"tagPattern,omitempty"`
	Tags       string `json:"tags,omitempty"`
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
	if r.Source.Name == "" {
		return fmt.Errorf("source needs the package name")
	}
	// The name becomes a folder in the store; it must stay one folder there.
	if strings.ContainsAny(r.Source.Name, `/\`) || r.Source.Name == "." || r.Source.Name == ".." {
		return fmt.Errorf("package name %q must not be a path", r.Source.Name)
	}
	switch r.Source.Type {
	case SourceNPM:
		if r.Source.Registry == "" {
			return fmt.Errorf("npm source needs registry")
		}
		if r.Path != "" || len(r.Paths) > 0 || r.Source.URL != "" || r.Source.TagPattern != "" || r.Source.Tags != "" {
			return fmt.Errorf("npm source takes no path, paths, url, tagPattern or tags")
		}
	case SourceGitHub:
		if r.Source.URL == "" {
			return fmt.Errorf("github source needs url")
		}
		if r.Source.Registry != "" {
			return fmt.Errorf("github source takes no registry")
		}
		if _, err := path.Match(r.Source.TagPattern, ""); err != nil {
			return fmt.Errorf("tagPattern: %w", err)
		}
		tags, err := semver.ParseRange(r.Source.Tags)
		if err != nil {
			return fmt.Errorf("tags: %w", err)
		}
		r.tags = tags
		if err := r.checkPaths(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown source type %q", r.Source.Type)
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
		if err := checkVPMDependencies(o.Set["vpmDependencies"]); err != nil {
			return fmt.Errorf("overrides[%d].set.vpmDependencies: %w", i, err)
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

func checkFolder(p string) error {
	clean := path.Clean("/" + p)
	if strings.Contains(p, "\\") || clean != "/"+strings.Trim(p, "/") {
		return fmt.Errorf("bad folder %q", p)
	}
	return nil
}

func (r *Recipe) checkPaths() error {
	if r.Path != "" && len(r.Paths) > 0 {
		return fmt.Errorf("write either path or paths, not both")
	}
	if err := checkFolder(r.Path); err != nil {
		return fmt.Errorf("path: %w", err)
	}
	for i, p := range r.Paths {
		if p.Path == nil {
			return fmt.Errorf(`paths[%d] has no "path" ("" is the repository root)`, i)
		}
		if err := checkFolder(*p.Path); err != nil {
			return fmt.Errorf("paths[%d]: %w", i, err)
		}
		tags, err := semver.ParseRange(p.Tags)
		if err != nil {
			return fmt.Errorf("paths[%d].tags: %w", i, err)
		}
		// Each tag has one folder, so the ranges must not overlap.
		for j, prev := range r.paths {
			if semver.Overlaps(prev.tags, tags) {
				return fmt.Errorf("paths[%d].tags %q overlaps paths[%d].tags %q", i, p.Tags, j, prev.tags)
			}
		}
		r.paths = append(r.paths, parsedPathRule{tags: tags, path: *p.Path})
	}
	return nil
}

// AcceptsTag reports whether the recipe reads a tag whose name holds
// version v (Git sources).
func (r *Recipe) AcceptsTag(v semver.Version) bool {
	return r.tags.Contains(v)
}

// PathFor returns the folder of the package in the tag whose name holds
// version v, and false when no folder is given for the tag (Git sources).
func (r *Recipe) PathFor(v semver.Version) (string, bool) {
	if len(r.paths) == 0 {
		return r.Path, true
	}
	for _, p := range r.paths {
		if p.tags.Contains(v) {
			return p.path, true
		}
	}
	return "", false
}

// ListingKey identifies what decides the versions the recipe's version list
// holds: the source, the folders and the range (Versions), which `update`
// applies when it reads upstream (kaikoga 2026-11-18). A version list kept
// from an update whose key differs is stale. Prerelease and Overrides are
// not in it, because they are applied to the list when it is read.
func (r *Recipe) ListingKey() string {
	data, _ := json.Marshal(struct {
		Source   Source     `json:"source"`
		Path     string     `json:"path"`
		Paths    []PathRule `json:"paths"`
		Versions string     `json:"versions"`
	}{r.Source, r.Path, r.Paths, r.Versions})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

// InRange reports whether version v is in the recipe's range, which is the
// rule for what Vpmify takes from the source. Prereleases in range are
// taken too, for a user who names one.
func (r *Recipe) InRange(v semver.Version) bool {
	return r.versions.Contains(v)
}

// Accepts reports whether pull chooses version v without its being named:
// in range, and stable unless the recipe takes prereleases.
func (r *Recipe) Accepts(v semver.Version) bool {
	if v.IsPrerelease() && !r.Prerelease {
		return false
	}
	return r.versions.Contains(v)
}

// OverridesFor returns the field sets that apply to v, in recipe order, as
// the recipe writes them.
func (r *Recipe) OverridesFor(v semver.Version) []map[string]json.RawMessage {
	var out []map[string]json.RawMessage
	for _, o := range r.overrides {
		if o.versions.Contains(v) {
			out = append(out, o.set)
		}
	}
	return out
}

// Placeholders in the values of vpmDependencies in overrides. Vpmify writes
// only the dependencies a recipe names, and takes no UPM dependency on its
// own (kaikoga 2026-11-18): which UPM dependency a VPM package needs, and in
// which version, is the recipe's call.
var (
	placeholderPattern = regexp.MustCompile(`\{[^{}]*\}`)
	placeholders       = map[string]bool{
		// The package's own version, for packages released in lockstep.
		"{version}": true,
		// The version the upstream package.json's UPM "dependencies" gives
		// the same package, for a dependency whose numbers differ.
		"{dependencies}": true,
	}
)

func checkVPMDependencies(raw json.RawMessage) error {
	if raw == nil || string(raw) == "null" {
		return nil
	}
	var deps map[string]string
	if err := json.Unmarshal(raw, &deps); err != nil {
		return fmt.Errorf("want an object of version strings: %w", err)
	}
	for name, value := range deps {
		for _, p := range placeholderPattern.FindAllString(value, -1) {
			if !placeholders[p] {
				return fmt.Errorf("%s: unknown placeholder %s (want {version} or {dependencies})", name, p)
			}
		}
	}
	return nil
}

// ExpandedOverridesFor returns OverridesFor(v) with the placeholders in
// vpmDependencies filled in from v and upstream, the UPM "dependencies" of
// version v.
func (r *Recipe) ExpandedOverridesFor(v semver.Version, upstream map[string]string) ([]map[string]json.RawMessage, error) {
	var out []map[string]json.RawMessage
	for _, set := range r.OverridesFor(v) {
		raw, ok := set["vpmDependencies"]
		if !ok || string(raw) == "null" {
			out = append(out, set)
			continue
		}
		var deps map[string]string
		if err := json.Unmarshal(raw, &deps); err != nil {
			return nil, err
		}
		for name, value := range deps {
			if strings.Contains(value, "{dependencies}") {
				dep, ok := upstream[name]
				if !ok {
					return nil, fmt.Errorf("vpmDependencies %s uses {dependencies}, but the upstream package.json has no UPM dependency on %s", name, name)
				}
				value = strings.ReplaceAll(value, "{dependencies}", dep)
			}
			deps[name] = strings.ReplaceAll(value, "{version}", v.String())
		}
		// Without HTML escaping, ">=" stays as people write it in
		// package.json instead of becoming ">=".
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(deps); err != nil {
			return nil, err
		}
		expanded := json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n"))
		copied := make(map[string]json.RawMessage, len(set))
		for k, val := range set {
			copied[k] = val
		}
		copied["vpmDependencies"] = expanded
		out = append(out, copied)
	}
	return out, nil
}

// convertRevision changes when Vpmify starts to convert differently, so that
// versions made available by an older Vpmify are built again. 3: UPM
// dependencies are no longer copied into vpmDependencies on their own.
const convertRevision = 3

// Fingerprint identifies how version v is built from upstream: from the
// folder dir of the archive, by this recipe. An available version whose
// fingerprint differs is built again when its recipe is pulled.
func (r *Recipe) Fingerprint(v semver.Version, dir string) string {
	h := sha256.New()
	// encoding/json writes map keys sorted, so this is stable.
	data, _ := json.Marshal(struct {
		Revision  int                          `json:"revision"`
		Source    Source                       `json:"source"`
		Dir       string                       `json:"dir"`
		Overrides []map[string]json.RawMessage `json:"overrides"`
	}{convertRevision, r.Source, dir, r.OverridesFor(v)})
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
