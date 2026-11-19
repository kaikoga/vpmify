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

// Recipe describes packages, where they come from and how to convert them.
// A recipe has sources so that one recipe can make a package available
// together with the packages it depends on, and a source has packages so
// that packages released together from one repository can name each other's
// version in the same tag (kaikoga 2026-11-19). Which versions to make
// available is not part of it: the user chooses that with each command
// (kaikoga 2026-11-18).
type Recipe struct {
	ID      string    `json:"id"`
	Sources []*Source `json:"sources"`
}

// Source says where packages come from.
type Source struct {
	Type string `json:"type"`
	// Registry is for npm-compatible registries.
	Registry string `json:"registry,omitempty"`
	// URL, TagPattern, TagVersion and Tags are for Git repositories.
	// TagPattern is a glob on tag names (default "*"). Tags is a range on the
	// version of the tag (its name with a leading "v" dropped, or as
	// TagVersion reads it), which limits the tags Vpmify reads; it differs
	// from Package.Versions, the range on the package's version, because a
	// repository's tags need not follow its packages' versions.
	URL        string      `json:"url,omitempty"`
	TagPattern string      `json:"tagPattern,omitempty"`
	TagVersion *TagVersion `json:"tagVersion,omitempty"`
	Tags       string      `json:"tags,omitempty"`
	Packages   []*Package  `json:"packages"`

	tags       semver.Range
	tagVersion *tagVersionRule
}

// Package is one package a source provides, and how to convert it.
type Package struct {
	// Name is the package name, as package.json writes it.
	Name string `json:"name"`
	// Path is the folder of the package inside a Git repository, for every
	// tag. Empty means the repository root.
	Path string `json:"path,omitempty"`
	// Paths give the folder by tag instead, for a package that moved (UniVRM
	// moved Assets/VRM to Packages/VRM in v0.131.0). The ranges are on the
	// tag's version, not the package's: the package's version is in the
	// package.json that the folder holds (unless VersionFromTag). A tag that
	// no range takes is not read.
	Paths []PathRule `json:"paths,omitempty"`
	// Versions and Prerelease say which versions the recipe can convert.
	Versions   string `json:"versions,omitempty"`
	Prerelease bool   `json:"prerelease,omitempty"`
	// VersionFromTag makes the version of the tag the package's version, in
	// place of the one its package.json has, for a repository whose
	// package.json does not tell its releases apart (Warudo-Mod-Tool keeps
	// 0.15.2 for tags 0.15.2.1 and 0.15.2.2). Vpmify writes it into
	// package.json. It is nil when the recipe writes false or nothing.
	VersionFromTag *VersionFromTag `json:"versionFromTag,omitempty"`
	Overrides      []Override      `json:"overrides,omitempty"`

	versions  semver.Range
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

// ValidID reports whether id can be a recipe's id. Such an id names no
// folder above the recipe files.
func ValidID(id string) bool {
	return idPattern.MatchString(id)
}

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
		if strings.Contains(err.Error(), `unknown field "source"`) {
			return nil, fmt.Errorf("%w (recipes list their sources under \"sources\" since 2026-11-19; write the recipe in that form and add it again)", err)
		}
		return nil, err
	}
	if err := r.check(); err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *Recipe) check() error {
	if !ValidID(r.ID) {
		return fmt.Errorf("id %q must be lowercase letters, digits, '.', '_' or '-'", r.ID)
	}
	if len(r.Sources) == 0 {
		return fmt.Errorf("recipe has no sources")
	}
	// Each package has its folders in the recipe's part of the store, by
	// name, so a name may come only once in a recipe.
	seen := map[string]bool{}
	for i, s := range r.Sources {
		if s == nil {
			return fmt.Errorf("sources[%d] is null", i)
		}
		if err := s.check(); err != nil {
			return fmt.Errorf("sources[%d]: %w", i, err)
		}
		for _, p := range s.Packages {
			if seen[p.Name] {
				return fmt.Errorf("package %s comes twice in the recipe", p.Name)
			}
			seen[p.Name] = true
		}
	}
	return nil
}

func (s *Source) check() error {
	if len(s.Packages) == 0 {
		return fmt.Errorf("source has no packages")
	}
	switch s.Type {
	case SourceNPM:
		if s.Registry == "" {
			return fmt.Errorf("npm source needs registry")
		}
		if s.URL != "" || s.TagPattern != "" || s.TagVersion != nil || s.Tags != "" {
			return fmt.Errorf("npm source takes no url, tagPattern, tagVersion or tags")
		}
	case SourceGitHub:
		if s.URL == "" {
			return fmt.Errorf("github source needs url")
		}
		if s.Registry != "" {
			return fmt.Errorf("github source takes no registry")
		}
		if _, err := path.Match(s.TagPattern, ""); err != nil {
			return fmt.Errorf("tagPattern: %w", err)
		}
		if s.TagVersion != nil {
			rule, err := parseTagVersion(s.TagVersion)
			if err != nil {
				return fmt.Errorf("tagVersion: %w", err)
			}
			s.tagVersion = rule
		}
		tags, err := semver.ParseRange(s.Tags)
		if err != nil {
			return fmt.Errorf("tags: %w", err)
		}
		s.tags = tags
	default:
		return fmt.Errorf("unknown source type %q", s.Type)
	}
	names := map[string]bool{}
	for _, p := range s.Packages {
		if p != nil {
			names[p.Name] = true
		}
	}
	for i, p := range s.Packages {
		if p == nil {
			return fmt.Errorf("packages[%d] is null", i)
		}
		if err := p.check(s, names); err != nil {
			if p.Name != "" {
				return fmt.Errorf("package %s: %w", p.Name, err)
			}
			return fmt.Errorf("packages[%d]: %w", i, err)
		}
	}
	return nil
}

func (p *Package) check(s *Source, siblings map[string]bool) error {
	if p.Name == "" {
		return fmt.Errorf("package needs a name")
	}
	// The name becomes a folder in the store; it must stay one folder there.
	if strings.ContainsAny(p.Name, `/\`) || p.Name == "." || p.Name == ".." {
		return fmt.Errorf("package name %q must not be a path", p.Name)
	}
	if f := p.VersionFromTag; f != nil && !f.All && f.Tags == "" {
		p.VersionFromTag = nil
	}
	switch s.Type {
	case SourceNPM:
		if p.Path != "" || len(p.Paths) > 0 || p.VersionFromTag != nil {
			return fmt.Errorf("an npm source's packages take no path, paths or versionFromTag")
		}
	case SourceGitHub:
		if err := p.checkPaths(); err != nil {
			return err
		}
		if f := p.VersionFromTag; f != nil && !f.All {
			tags, err := semver.ParseRange(f.Tags)
			if err != nil {
				return fmt.Errorf("versionFromTag.tags: %w", err)
			}
			f.tags = tags
		}
	}
	rng, err := semver.ParseRange(p.Versions)
	if err != nil {
		return fmt.Errorf("versions: %w", err)
	}
	p.versions = rng
	for i, o := range p.Overrides {
		rng, err := semver.ParseRange(o.Versions)
		if err != nil {
			return fmt.Errorf("overrides[%d].versions: %w", i, err)
		}
		if len(o.Set) == 0 {
			return fmt.Errorf("overrides[%d].set is empty", i)
		}
		if err := checkVPMDependencies(o.Set["vpmDependencies"], s.Type == SourceGitHub, p.Name, siblings); err != nil {
			return fmt.Errorf("overrides[%d].set.vpmDependencies: %w", i, err)
		}
		for k := range o.Set {
			if protectedFields[k] {
				return fmt.Errorf("overrides[%d] must not set %q", i, k)
			}
		}
		p.overrides = append(p.overrides, parsedOverride{versions: rng, set: o.Set})
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

func (p *Package) checkPaths() error {
	if p.Path != "" && len(p.Paths) > 0 {
		return fmt.Errorf("write either path or paths, not both")
	}
	if err := checkFolder(p.Path); err != nil {
		return fmt.Errorf("path: %w", err)
	}
	for i, rule := range p.Paths {
		if rule.Path == nil {
			return fmt.Errorf(`paths[%d] has no "path" ("" is the repository root)`, i)
		}
		if err := checkFolder(*rule.Path); err != nil {
			return fmt.Errorf("paths[%d]: %w", i, err)
		}
		tags, err := semver.ParseRange(rule.Tags)
		if err != nil {
			return fmt.Errorf("paths[%d].tags: %w", i, err)
		}
		// Each tag has one folder, so the ranges must not overlap.
		for j, prev := range p.paths {
			if semver.Overlaps(prev.tags, tags) {
				return fmt.Errorf("paths[%d].tags %q overlaps paths[%d].tags %q", i, rule.Tags, j, prev.tags)
			}
		}
		p.paths = append(p.paths, parsedPathRule{tags: tags, path: *rule.Path})
	}
	return nil
}

// Describe names the source in messages: the Git repository's URL, or
// "npm" and the registry.
func (s *Source) Describe() string {
	switch s.Type {
	case SourceNPM:
		return "npm " + s.Registry
	case SourceGitHub:
		return s.URL
	}
	return s.Type
}

// Names returns the names of the source's packages, in recipe order.
func (s *Source) Names() []string {
	out := make([]string, 0, len(s.Packages))
	for _, p := range s.Packages {
		out = append(out, p.Name)
	}
	return out
}

// Names returns the names of every package the recipe provides, in recipe
// order.
func (r *Recipe) Names() []string {
	var out []string
	for _, s := range r.Sources {
		out = append(out, s.Names()...)
	}
	return out
}

// Package returns the package name and its source, or nil when the recipe
// does not provide it.
func (r *Recipe) Package(name string) (*Source, *Package) {
	for _, s := range r.Sources {
		for _, p := range s.Packages {
			if p.Name == name {
				return s, p
			}
		}
	}
	return nil, nil
}

// AcceptsTag reports whether the source reads a tag whose version, as
// TagVersionOf reads it, is v (Git sources).
func (s *Source) AcceptsTag(v semver.Version) bool {
	return s.tags.Contains(v)
}

// PathFor returns the folder of the package in the tag whose version is v,
// and false when no folder is given for the tag (Git sources).
func (p *Package) PathFor(v semver.Version) (string, bool) {
	if len(p.paths) == 0 {
		return p.Path, true
	}
	for _, rule := range p.paths {
		if rule.tags.Contains(v) {
			return rule.path, true
		}
	}
	return "", false
}

// sourceFields are the fields of a source without its packages.
// TagVersion is omitted when empty, so that the keys and fingerprints of
// recipes without it stay what they were before it came.
type sourceFields struct {
	Type       string      `json:"type"`
	Registry   string      `json:"registry"`
	URL        string      `json:"url"`
	TagPattern string      `json:"tagPattern"`
	Tags       string      `json:"tags"`
	TagVersion *TagVersion `json:"tagVersion,omitempty"`
}

func (s *Source) fields() sourceFields {
	return sourceFields{s.Type, s.Registry, s.URL, s.TagPattern, s.Tags, s.TagVersion}
}

// ListingKey identifies what decides the versions the source's version list
// holds: the source, and each package's name, folders, range (Versions) and
// VersionFromTag, which `update` applies when it reads upstream (kaikoga 2026-11-18). A
// version list kept from an update whose key differs is stale. Prerelease
// and Overrides are not in it, because they are applied to the list when it
// is read; {sameTag} is too, from the versions the list keeps for each tag.
func (s *Source) ListingKey() string {
	type pkg struct {
		Name           string          `json:"name"`
		Path           string          `json:"path"`
		Paths          []PathRule      `json:"paths"`
		Versions       string          `json:"versions"`
		VersionFromTag *VersionFromTag `json:"versionFromTag,omitempty"`
	}
	var pkgs []pkg
	for _, p := range s.Packages {
		pkgs = append(pkgs, pkg{p.Name, p.Path, p.Paths, p.Versions, p.VersionFromTag})
	}
	data, _ := json.Marshal(struct {
		Source   sourceFields `json:"source"`
		Packages []pkg        `json:"packages"`
	}{s.fields(), pkgs})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

// InRange reports whether version v is in the package's range, which is the
// rule for what Vpmify takes from the source. Prereleases in range are
// taken too, for a user who names one.
func (p *Package) InRange(v semver.Version) bool {
	return p.versions.Contains(v)
}

// Accepts reports whether pull chooses version v without its being named:
// in range, and stable unless the package takes prereleases.
func (p *Package) Accepts(v semver.Version) bool {
	if v.IsPrerelease() && !p.Prerelease {
		return false
	}
	return p.versions.Contains(v)
}

// OverridesFor returns the field sets that apply to v, in recipe order, as
// the recipe writes them.
func (p *Package) OverridesFor(v semver.Version) []map[string]json.RawMessage {
	var out []map[string]json.RawMessage
	for _, o := range p.overrides {
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
		// The version the same package has in the same tag, for packages of
		// one repository whose numbers differ and whose UPM dependencies
		// cannot be trusted (kaikoga 2026-11-19). Only a package of the same
		// source can be named: there is no tag across sources.
		"{sameTag}": true,
	}
)

func checkVPMDependencies(raw json.RawMessage, tagged bool, self string, siblings map[string]bool) error {
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
				return fmt.Errorf("%s: unknown placeholder %s (want {version}, {dependencies} or {sameTag})", name, p)
			}
			if p != "{sameTag}" {
				continue
			}
			switch {
			case !tagged:
				return fmt.Errorf("%s: {sameTag} needs a source with tags", name)
			case name == self:
				return fmt.Errorf("%s: {sameTag} names the package itself", name)
			case !siblings[name]:
				return fmt.Errorf("%s: {sameTag} names only a package of the same source", name)
			}
		}
	}
	return nil
}

// ExpandedOverridesFor returns OverridesFor(v) with the placeholders in
// vpmDependencies filled in: from v, from upstream, the UPM "dependencies"
// of version v, and from sameTag, the versions of the source's other
// packages in the tag v was taken from that can be made available. A
// placeholder that cannot be filled in is an error, and the version cannot
// be converted.
func (p *Package) ExpandedOverridesFor(v semver.Version, upstream, sameTag map[string]string) ([]map[string]json.RawMessage, error) {
	var out []map[string]json.RawMessage
	for _, set := range p.OverridesFor(v) {
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
			if strings.Contains(value, "{sameTag}") {
				dep, ok := sameTag[name]
				if !ok {
					return nil, fmt.Errorf("vpmDependencies %s uses {sameTag}, but the same tag has no version of %s in its version list", name, name)
				}
				value = strings.ReplaceAll(value, "{sameTag}", dep)
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
// dependencies are no longer copied into vpmDependencies on their own. 4:
// the fingerprint takes the archive and the expanded overrides.
const convertRevision = 4

// Fingerprint identifies how a version of package p is built from upstream:
// from folder dir of the archive cacheKey, with the overrides as expanded
// for it. An available version whose fingerprint differs is built again
// when its recipe is pulled. The source's other packages are not in it, so
// that editing one package does not build the others again; what they give
// through {sameTag} is, in expanded.
func (s *Source) Fingerprint(p *Package, dir, cacheKey string, expanded []map[string]json.RawMessage) string {
	h := sha256.New()
	// encoding/json writes map keys sorted, so this is stable.
	data, _ := json.Marshal(struct {
		Revision  int                          `json:"revision"`
		Source    sourceFields                 `json:"source"`
		Name      string                       `json:"name"`
		Dir       string                       `json:"dir"`
		Archive   string                       `json:"archive"`
		Overrides []map[string]json.RawMessage `json:"overrides"`
	}{convertRevision, s.fields(), p.Name, dir, cacheKey, expanded})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Unity returns the "unity" field of version v after overrides, given the
// upstream value.
func (p *Package) Unity(v semver.Version, upstream string) string {
	unity := upstream
	for _, set := range p.OverridesFor(v) {
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
