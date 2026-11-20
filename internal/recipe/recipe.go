// Package recipe reads recipes: the user's definition of what to convert,
// where it comes from, and how to fix its package.json.
package recipe

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
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
	// Format is the format the recipe is written in, which its writer
	// declares. A file without it is not a recipe, and a recipe in a format
	// newer than Format needs a newer Vpmify (kaikoga 2026-11-20).
	Format int `json:"vpmifyFormat"`
	// ID names the recipe in commands and in the store. It is in reverse
	// domain form, with the domain of whoever writes the recipe, as the ids
	// of VPM repositories are (kaikoga 2026-11-20); Vpmify does not check
	// the form.
	ID string `json:"id"`
	// DisplayName is the name people read, as package.json has one beside
	// its name.
	DisplayName string    `json:"displayName,omitempty"`
	Sources     []*Source `json:"sources"`
}

// Format is the newest recipe format this Vpmify reads. It goes up when a
// recipe written for the new format would be built wrongly by a Vpmify that
// does not know it, since fields Vpmify does not know are ignored without a
// word (kaikoga 2026-11-20): they hold what a recipe may say beside what
// Vpmify reads, such as "$schema".
const Format = 1

// Source says where packages come from.
type Source struct {
	Type string `json:"type"`
	// Registry is for npm-compatible registries.
	Registry string `json:"registry,omitempty"`
	// URL, TagVersion and Tags are for Git repositories. Tags is a range on
	// the version of the tag (its name with a leading "v" dropped, or as
	// TagVersion reads it), which limits the tags Vpmify reads; it differs
	// from Package.Versions, the range on the package's version, because a
	// repository's tags need not follow its packages' versions. There is no
	// pattern on tag names beside them: a tag that does not read as a version
	// is not read, and TagVersion's match chooses among names (kaikoga
	// 2026-11-20).
	URL        string      `json:"url,omitempty"`
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
	// Overlays put content from other repositories into folders of the
	// package (Git sources). GitHub's archives leave a Git submodule out as an
	// empty folder, and UniVRM before v0.124.2 has MToon as one. An overlay
	// is not a Git submodule but another source unpacked into a subfolder,
	// which the recipe names (kaikoga 2026-11-20).
	Overlays []Overlay `json:"overlays,omitempty"`

	versions  semver.Range
	paths     []parsedPathRule
	overrides []parsedOverride
	overlays  []parsedOverlay
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

// Override changes top-level fields of package.json for the versions in
// range.
type Override struct {
	Versions string `json:"versions,omitempty"`
	Edit
}

// Edit is what an override does to package.json. Set replaces a field, and
// a null value removes it. Merge adds keys to a field that is an object, or
// replaces their values, and makes the field when package.json has none, so
// that a recipe adds a dependency without copying upstream's (kaikoga
// 2026-11-20); a field whose keys must go takes Set. In one override Set
// comes before Merge.
type Edit struct {
	Set   map[string]json.RawMessage            `json:"set,omitempty"`
	Merge map[string]map[string]json.RawMessage `json:"merge,omitempty"`
}

type parsedOverride struct {
	versions semver.Range
	Edit
}

// Overlay puts commit Commit of the GitHub repository URL into folder Path
// of the package, for the tags in range. Tags is a range on the tag's
// version, as in paths. Path is relative to the package's folder, so that
// it stays when paths moves the package (kaikoga 2026-11-20); the folder
// must be in the archive, empty.
//
// Resolve "submodule" (the commit the tag's submodule at Path points at)
// and a missing URL (the submodule's) are planned but not read yet.
type Overlay struct {
	Tags    string `json:"tags,omitempty"`
	Path    string `json:"path"`
	URL     string `json:"url,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Resolve string `json:"resolve,omitempty"`
}

type parsedOverlay struct {
	tags semver.Range
	Overlay
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

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
	// Editors on Windows may save UTF-8 with a byte order mark, which
	// encoding/json does not take.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	// The format is read first, so that a recipe this Vpmify cannot read says
	// so instead of failing on whatever the newer format changed.
	var head struct {
		Format *int `json:"vpmifyFormat"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		var typeErr *json.UnmarshalTypeError
		if !errors.As(err, &typeErr) {
			return nil, err
		}
		if typeErr.Field != "vpmifyFormat" {
			return nil, fmt.Errorf("not a Vpmify recipe: not a JSON object")
		}
		return nil, fmt.Errorf("vpmifyFormat must be a whole number")
	}
	switch {
	case head.Format == nil:
		return nil, fmt.Errorf("not a Vpmify recipe: it has no vpmifyFormat")
	case *head.Format < 1:
		return nil, fmt.Errorf("vpmifyFormat %d is not a format of Vpmify recipes", *head.Format)
	case *head.Format > Format:
		return nil, fmt.Errorf("the recipe is in format %d, and this Vpmify reads up to format %d: a newer Vpmify is needed", *head.Format, Format)
	}
	var r Recipe
	if err := json.Unmarshal(data, &r); err != nil {
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
		if u, err := url.Parse(s.Registry); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("registry %q is not an http(s) URL", s.Registry)
		}
		if s.URL != "" || s.TagVersion != nil || s.Tags != "" {
			return fmt.Errorf("npm source takes no url, tagVersion or tags")
		}
	case SourceGitHub:
		if s.URL == "" {
			return fmt.Errorf("github source needs url")
		}
		if _, _, err := GitHubRepo(s.URL); err != nil {
			return fmt.Errorf("url: %w", err)
		}
		if s.Registry != "" {
			return fmt.Errorf("github source takes no registry")
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
		if p.Path != "" || len(p.Paths) > 0 || p.VersionFromTag != nil || len(p.Overlays) > 0 {
			return fmt.Errorf("an npm source's packages take no path, paths, versionFromTag or overlays")
		}
	case SourceGitHub:
		if err := p.checkPaths(); err != nil {
			return err
		}
		if err := p.checkOverlays(); err != nil {
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
		if len(o.Set) == 0 && len(o.Merge) == 0 {
			return fmt.Errorf("overrides[%d] has neither set nor merge", i)
		}
		if err := checkVPMDependencies(o.Set["vpmDependencies"], s.Type == SourceGitHub, p.Name, siblings); err != nil {
			return fmt.Errorf("overrides[%d].set.vpmDependencies: %w", i, err)
		}
		for k := range o.Set {
			if protectedFields[k] {
				return fmt.Errorf("overrides[%d] must not set %q", i, k)
			}
		}
		for field, keys := range o.Merge {
			if protectedFields[field] {
				return fmt.Errorf("overrides[%d] must not merge into %q", i, field)
			}
			for k, raw := range keys {
				// What null does in merge is not decided: it may come to
				// remove the key, as it removes the field in set.
				if string(raw) == "null" {
					return fmt.Errorf("overrides[%d].merge.%s.%s is null: merge adds keys, and set replaces the field", i, field, k)
				}
			}
		}
		if keys, ok := o.Merge["vpmDependencies"]; ok {
			raw, err := json.Marshal(keys)
			if err != nil {
				return err
			}
			if err := checkVPMDependencies(raw, s.Type == SourceGitHub, p.Name, siblings); err != nil {
				return fmt.Errorf("overrides[%d].merge.vpmDependencies: %w", i, err)
			}
		}
		p.overrides = append(p.overrides, parsedOverride{versions: rng, Edit: o.Edit})
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
	// "Assets/Pkg/" is the folder "Assets/Pkg": the slashes would make
	// another URL of its package.json, and another listing key.
	p.Path = strings.Trim(p.Path, "/")
	for i, rule := range p.Paths {
		if rule.Path == nil {
			return fmt.Errorf(`paths[%d] has no "path" ("" is the repository root)`, i)
		}
		if err := checkFolder(*rule.Path); err != nil {
			return fmt.Errorf("paths[%d]: %w", i, err)
		}
		trimmed := strings.Trim(*rule.Path, "/")
		rule.Path = &trimmed
		p.Paths[i].Path = &trimmed
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

func (p *Package) checkOverlays() error {
	for i, o := range p.Overlays {
		// "" would be the package's folder itself, package.json and all.
		if strings.Trim(o.Path, "/") == "" {
			return fmt.Errorf(`overlays[%d] needs a "path" inside the package`, i)
		}
		if err := checkFolder(o.Path); err != nil {
			return fmt.Errorf("overlays[%d]: %w", i, err)
		}
		o.Path = strings.Trim(o.Path, "/")
		if o.Resolve != "" {
			return fmt.Errorf("overlays[%d]: resolve is not supported yet; write url and commit", i)
		}
		if o.URL == "" {
			return fmt.Errorf("overlays[%d] needs url", i)
		}
		if _, _, err := GitHubRepo(o.URL); err != nil {
			return fmt.Errorf("overlays[%d].url: %w", i, err)
		}
		if !commitPattern.MatchString(o.Commit) {
			return fmt.Errorf("overlays[%d].commit %q is not a commit in 40 lowercase hex digits", i, o.Commit)
		}
		tags, err := semver.ParseRange(o.Tags)
		if err != nil {
			return fmt.Errorf("overlays[%d].tags: %w", i, err)
		}
		// One folder takes one content in a tag. Different folders may share
		// tags, but not when one holds the other.
		for j, prev := range p.overlays {
			if semver.Overlaps(prev.tags, tags) && nestedFolders(prev.Path, o.Path) {
				return fmt.Errorf("overlays[%d] and overlays[%d] both fill %q for some tags", j, i, o.Path)
			}
		}
		p.overlays = append(p.overlays, parsedOverlay{tags: tags, Overlay: o})
	}
	return nil
}

// GitHubRepo returns the owner and the repository that raw names, the URL
// of a github source or of an overlay. Only https://github.com/<owner>/<repo>
// is taken, with or without ".git": Vpmify reads the repository through
// GitHub's own endpoints, which it addresses by owner and repository.
func GitHubRepo(raw string) (owner, repo string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("want https://github.com/<owner>/<repo>, not %q", raw)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

// nestedFolders reports whether a and b are one folder or one holds the
// other.
func nestedFolders(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
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

// OverlaysFor returns the overlays of the tag whose version is v, in the
// recipe's order, with their paths trimmed of slashes (Git sources).
func (p *Package) OverlaysFor(v semver.Version) []Overlay {
	var out []Overlay
	for _, o := range p.overlays {
		if o.tags.Contains(v) {
			out = append(out, o.Overlay)
		}
	}
	return out
}

// sourceFields are the fields of a source without its packages.
type sourceFields struct {
	Type       string      `json:"type"`
	Registry   string      `json:"registry"`
	URL        string      `json:"url"`
	Tags       string      `json:"tags"`
	TagVersion *TagVersion `json:"tagVersion,omitempty"`
}

func (s *Source) fields() sourceFields {
	return sourceFields{s.Type, s.Registry, s.URL, s.Tags, s.TagVersion}
}

// ListingKey identifies what decides the versions the source's version list
// holds: the source, and each package's name, folders, range (Versions),
// VersionFromTag and Overlays, which `update` applies when it reads upstream
// (kaikoga 2026-11-18; overlays are applied there because the list keeps no
// tag versions, and so that pull reads the list alone, kaikoga 2026-11-20).
// A version list kept from an update whose key differs is stale. Prerelease
// and Overrides are not in it, because they are applied to the list when it
// is read; {sameTag} is too, from the versions the list keeps for each tag.
func (s *Source) ListingKey() string {
	type pkg struct {
		Name           string          `json:"name"`
		Path           string          `json:"path"`
		Paths          []PathRule      `json:"paths"`
		Versions       string          `json:"versions"`
		VersionFromTag *VersionFromTag `json:"versionFromTag,omitempty"`
		Overlays       []Overlay       `json:"overlays,omitempty"`
	}
	var pkgs []pkg
	for _, p := range s.Packages {
		pkgs = append(pkgs, pkg{p.Name, p.Path, p.Paths, p.Versions, p.VersionFromTag, p.Overlays})
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

// OverridesFor returns the edits that apply to v, in recipe order, as the
// recipe writes them.
func (p *Package) OverridesFor(v semver.Version) []Edit {
	var out []Edit
	for _, o := range p.overrides {
		if o.versions.Contains(v) {
			out = append(out, o.Edit)
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
		// The version the package's UPM "dependencies" give the same package,
		// for a dependency whose numbers differ: the upstream package.json's,
		// or what the recipe's overrides set in their place, so that a recipe
		// that corrects a UPM dependency writes the version once (kaikoga
		// 2026-11-20).
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
// vpmDependencies filled in: from v, from the UPM "dependencies" of version
// v, which are upstream unless an override sets the field, and from sameTag,
// the versions of the source's other packages in the tag v was taken from
// that can be made available. A placeholder that cannot be filled in is an
// error, and the version cannot be converted.
func (p *Package) ExpandedOverridesFor(v semver.Version, upstream, sameTag map[string]string) ([]Edit, error) {
	edits := p.OverridesFor(v)
	// {dependencies} reads the field as package.json has it once every
	// override is applied, wherever in the overrides it is set or merged
	// into.
	deps := upstream
	for _, e := range edits {
		if raw, ok := e.Set["dependencies"]; ok {
			// null removes the field, and what is not an object of version
			// strings names no version.
			deps = nil
			if err := json.Unmarshal(raw, &deps); err != nil {
				deps = nil
			}
		}
		if keys := e.Merge["dependencies"]; len(keys) > 0 {
			merged := make(map[string]string, len(deps)+len(keys))
			maps.Copy(merged, deps)
			for name, raw := range keys {
				var value string
				if err := json.Unmarshal(raw, &value); err != nil {
					delete(merged, name)
					continue
				}
				merged[name] = value
			}
			deps = merged
		}
	}
	fill := func(name, value string) (string, error) {
		if strings.Contains(value, "{dependencies}") {
			dep, ok := deps[name]
			if !ok {
				return "", fmt.Errorf("vpmDependencies %s uses {dependencies}, but package.json has no UPM dependency on %s", name, name)
			}
			value = strings.ReplaceAll(value, "{dependencies}", dep)
		}
		if strings.Contains(value, "{sameTag}") {
			dep, ok := sameTag[name]
			if !ok {
				return "", fmt.Errorf("vpmDependencies %s uses {sameTag}, but the same tag has no version of %s in its version list", name, name)
			}
			value = strings.ReplaceAll(value, "{sameTag}", dep)
		}
		return strings.ReplaceAll(value, "{version}", v.String()), nil
	}
	var out []Edit
	for _, e := range edits {
		if raw, ok := e.Set["vpmDependencies"]; ok && string(raw) != "null" {
			var values map[string]string
			if err := json.Unmarshal(raw, &values); err != nil {
				return nil, err
			}
			for name, value := range values {
				filled, err := fill(name, value)
				if err != nil {
					return nil, err
				}
				values[name] = filled
			}
			expanded, err := marshalPlain(values)
			if err != nil {
				return nil, err
			}
			e.Set = maps.Clone(e.Set)
			e.Set["vpmDependencies"] = expanded
		}
		if keys, ok := e.Merge["vpmDependencies"]; ok {
			expanded := make(map[string]json.RawMessage, len(keys))
			for name, raw := range keys {
				var value string
				if err := json.Unmarshal(raw, &value); err != nil {
					return nil, err
				}
				filled, err := fill(name, value)
				if err != nil {
					return nil, err
				}
				if expanded[name], err = marshalPlain(filled); err != nil {
					return nil, err
				}
			}
			e.Merge = maps.Clone(e.Merge)
			e.Merge["vpmDependencies"] = expanded
		}
		out = append(out, e)
	}
	return out, nil
}

// marshalPlain writes v as JSON without HTML escaping, so that ">=" stays
// as people write it in package.json instead of becoming a Unicode escape.
func marshalPlain(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// convertRevision changes when Vpmify starts to convert differently, so that
// versions made available by an older Vpmify are built again. 3: UPM
// dependencies are no longer copied into vpmDependencies on their own. 4:
// the fingerprint takes the archive and the expanded overrides. 5: the
// overrides in it are edits, with set and merge.
const convertRevision = 5

// BuiltOverlay is an overlay as a version is built with it: the folder in
// the package and the archive its content comes from.
type BuiltOverlay struct {
	Path    string `json:"path"`
	Archive string `json:"archive"`
}

// Fingerprint identifies how a version of package p is built from upstream:
// from folder dir of the archive cacheKey, with the overrides as expanded
// for it and the overlays. An available version whose fingerprint differs
// is built again when its recipe is pulled. The source's other packages are
// not in it, so that editing one package does not build the others again;
// what they give through {sameTag} is, in expanded. Overlays are omitted
// when there are none, so that versions built before they came keep their
// fingerprints.
func (s *Source) Fingerprint(p *Package, dir, cacheKey string, expanded []Edit, overlays []BuiltOverlay) string {
	h := sha256.New()
	// encoding/json writes map keys sorted, so this is stable.
	data, _ := json.Marshal(struct {
		Revision  int            `json:"revision"`
		Source    sourceFields   `json:"source"`
		Name      string         `json:"name"`
		Dir       string         `json:"dir"`
		Archive   string         `json:"archive"`
		Overrides []Edit         `json:"overrides"`
		Overlays  []BuiltOverlay `json:"overlays,omitempty"`
	}{convertRevision, s.fields(), p.Name, dir, cacheKey, expanded, overlays})
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Unity returns the "unity" field of version v after overrides, given the
// upstream value.
func (p *Package) Unity(v semver.Version, upstream string) string {
	unity := upstream
	for _, e := range p.OverridesFor(v) {
		raw, ok := e.Set["unity"]
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
