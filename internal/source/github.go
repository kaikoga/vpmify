package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/fsutil"
	"github.com/kaikoga/vpmify/internal/recipe"
	"github.com/kaikoga/vpmify/internal/semver"
)

// GitHub is packages in a GitHub repository, versioned by tags.
//
// Tags come from Git's smart HTTP ref advertisement, which needs neither a
// git binary nor the REST API and its 60-requests-an-hour limit for
// anonymous clients. package.json comes from raw.githubusercontent.com and
// archives from codeload.github.com, both addressed by commit.
type GitHub struct {
	Owner, Repo string
	Packages    []GitHubPackage
	// TagVersion reads the version of a tag from its name; false means the
	// tag is not read.
	TagVersion func(name string) (semver.Version, bool)
	AcceptTag  func(semver.Version) bool
	Client     *fetch.Client
	// CacheDir keeps each commit's package.json, which never changes.
	CacheDir string

	// Endpoints, replaceable in tests.
	GitBase, RawBase, CodeloadBase string
}

// GitHubPackage is one package of the repository.
type GitHubPackage struct {
	// Name is the name package.json must have.
	Name string
	// PathFor returns the folder of the package in a tag, from the tag's
	// version; false means the tag is not read.
	PathFor func(semver.Version) (string, bool)
	// VersionFromTag reports whether the package takes the version of a
	// tag, given that version, in place of the one in its package.json. nil
	// is for no tag.
	VersionFromTag func(semver.Version) bool
	// OverlaysFor returns the overlays of a tag, from the tag's version. nil
	// is for none.
	OverlaysFor func(semver.Version) []recipe.Overlay
}

// NewGitHub returns the upstream of a github recipe source.
func NewGitHub(s *recipe.Source, client *fetch.Client, cacheDir string) (*GitHub, error) {
	owner, repo, err := recipe.GitHubRepo(s.URL)
	if err != nil {
		return nil, err
	}
	var pkgs []GitHubPackage
	for _, p := range s.Packages {
		pkgs = append(pkgs, GitHubPackage{Name: p.Name, PathFor: p.PathFor, VersionFromTag: p.TakesTagVersion, OverlaysFor: p.OverlaysFor})
	}
	return &GitHub{
		Owner: owner, Repo: repo,
		Packages:     pkgs,
		TagVersion:   s.TagVersionOf,
		AcceptTag:    s.AcceptsTag,
		Client:       client,
		CacheDir:     cacheDir,
		GitBase:      "https://github.com",
		RawBase:      "https://raw.githubusercontent.com",
		CodeloadBase: "https://codeload.github.com",
	}, nil
}

type tag struct {
	name    string
	version semver.Version
	commit  string
}

// Releases reads package.json of each package in every tag the source
// accepts, from one listing of the tags, and lists one release per package
// version. When a version appears in several tags, the oldest tag wins:
// that is where the version was first published, and what Vpmify converts
// never changes when newer tags keep the same version (kaikoga 2026-11-18).
// A stable version is taken from the oldest stable tag that has it when
// there is one, so that it is built from the release and its {sameTag}
// names the release's packages, not a release candidate's prereleases
// (kaikoga 2026-11-19). Each release keeps the versions the other packages
// have in its tag.
func (g *GitHub) Releases(ctx context.Context) (map[string][]Release, []string, error) {
	tags, err := g.tags(ctx)
	if err != nil {
		return nil, nil, err
	}
	n := len(g.Packages)
	found := make([]*Release, len(tags)*n)
	errs := make([]error, len(tags)*n)
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, t := range tags {
		for j, p := range g.Packages {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				found[i*n+j], errs[i*n+j] = g.release(ctx, t, p)
			}()
		}
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}
	var same sameVersionTags
	out := map[string][]Release{}
	for j, p := range g.Packages {
		// chosen is the tag each version is taken from. Versions that differ
		// only in build metadata, such as 1.0.0+a and 1.0.0+b, are one
		// version, as SemVer and ALCOM compare them: both in the user
		// packages would leave ALCOM to pick either.
		var chosen []int
		for i := range tags { // oldest first
			rel := found[i*n+j]
			if rel == nil {
				continue
			}
			c := slices.IndexFunc(chosen, func(k int) bool { return semver.Compare(found[k*n+j].Version, rel.Version) == 0 })
			switch {
			case c < 0:
				chosen = append(chosen, i)
			case !rel.Version.IsPrerelease() && tags[chosen[c]].version.IsPrerelease() && !tags[i].version.IsPrerelease():
				chosen[c] = i
			}
		}
		rels := []Release{}
		for _, i := range chosen {
			rel := found[i*n+j]
			var names []string
			for k, t := range tags {
				if semver.Compare(t.version, tags[i].version) == 0 && found[k*n+j] != nil && semver.Compare(found[k*n+j].Version, rel.Version) == 0 {
					names = append(names, t.name)
				}
			}
			same.add(names, tags[i], p.Name)
			for k, sibling := range g.Packages {
				if k == j || found[i*n+k] == nil {
					continue
				}
				if rel.SameTag == nil {
					rel.SameTag = map[string]string{}
				}
				rel.SameTag[sibling.Name] = found[i*n+k].Version.String()
			}
			rels = append(rels, *rel)
		}
		out[p.Name] = rels
	}
	for _, rels := range out {
		sort.SliceStable(rels, func(i, j int) bool { return semver.Compare(rels[i].Version, rels[j].Version) < 0 })
	}
	return out, same.warnings(), nil
}

// tags lists the tags whose name reads as a version the recipe accepts,
// oldest first.
func (g *GitHub) tags(ctx context.Context) ([]tag, error) {
	readVersion := g.TagVersion
	if readVersion == nil {
		readVersion = recipe.PlainTagVersion
	}
	u := fmt.Sprintf("%s/%s/%s.git/info/refs?service=git-upload-pack", g.GitBase, g.Owner, g.Repo)
	data, err := g.Client.GetBytes(ctx, u)
	if err != nil {
		return nil, err
	}
	refs, err := parseRefAdvertisement(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", u, err)
	}
	var out []tag
	for name, commit := range refs {
		tagName, ok := strings.CutPrefix(name, "refs/tags/")
		if !ok {
			continue
		}
		v, ok := readVersion(tagName)
		if !ok {
			continue
		}
		if g.AcceptTag != nil && !g.AcceptTag(v) {
			continue
		}
		out = append(out, tag{name: tagName, version: v, commit: commit})
	}
	// Tags of one version are in name order, numbers by value, which stands
	// for their age: the ref advertisement has no dates.
	sort.Slice(out, func(i, j int) bool {
		if c := semver.Compare(out[i].version, out[j].version); c != 0 {
			return c < 0
		}
		return naturalLess(out[i].name, out[j].name)
	})
	return out, nil
}

// sameVersionTags gathers the tags that read as one version and hold the
// same version of a package, which is taken from one of them, as for package
// versions that come in several tags: a repository whose owner did not write
// the recipe may add such tags, and they must not stop the recipe, but they
// are warned of (kaikoga 2026-11-19). The warning comes without a tagVersion
// rule too, for v1.2.3 beside 1.2.3 (kaikoga 2026-11-20). It names the tag
// each package is taken from, and the packages, as a tag may hold only some
// of them.
type sameVersionTags struct {
	keys  []string
	found map[string]*sameVersionTag
}

type sameVersionTag struct {
	names    []string
	used     tag
	packages []string
}

// add records that package pkg is taken from tag used, of the tags names.
func (s *sameVersionTags) add(names []string, used tag, pkg string) {
	if len(names) < 2 {
		return
	}
	key := strings.Join(names, "\x00") + "\x00\x00" + used.name
	if s.found == nil {
		s.found = map[string]*sameVersionTag{}
	}
	f, ok := s.found[key]
	if !ok {
		f = &sameVersionTag{names: names, used: used}
		s.found[key] = f
		s.keys = append(s.keys, key)
	}
	f.packages = append(f.packages, pkg)
}

func (s *sameVersionTags) warnings() []string {
	var out []string
	for _, key := range s.keys {
		f := s.found[key]
		verb := "is"
		if len(f.packages) > 1 {
			verb = "are"
		}
		out = append(out, fmt.Sprintf("tags %s read as version %s, and %s %s taken from %s; the recipe needs a tagVersion that tells them apart",
			joinNames(f.names), f.used.version, joinNames(f.packages), verb, f.used.name))
	}
	return out
}

// joinNames lists names as "a and b" or "a, b and c".
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// naturalLess orders names with their runs of digits compared by value, so
// that 0.15.2.9 comes before 0.15.2.10. Names equal by value (r1.0.0 and
// r01.0.0) are ordered by their text, so that the order, and the tag a
// version is taken from, does not follow the order the refs come in.
func naturalLess(a, b string) bool {
	if c := naturalCompare(a, b); c != 0 {
		return c < 0
	}
	return a < b
}

func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := digitRun(a), digitRun(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if len(na) != len(nb) {
				return len(na) - len(nb)
			}
			if c := strings.Compare(na, nb); c != 0 {
				return c
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return int(a[0]) - int(b[0])
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func digitRun(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}

// parseRefAdvertisement reads the pkt-line ref list of Git's smart HTTP
// protocol (version 0). For an annotated tag it returns the peeled commit.
func parseRefAdvertisement(data []byte) (map[string]string, error) {
	refs := map[string]string{}
	peeled := map[string]string{}
	for len(data) > 0 {
		if len(data) < 4 {
			return nil, errors.New("truncated pkt-line")
		}
		n, err := strconv.ParseUint(string(data[:4]), 16, 16)
		if err != nil {
			return nil, fmt.Errorf("bad pkt-line length %q", data[:4])
		}
		if n == 0 {
			data = data[4:]
			continue
		}
		if n < 4 || int(n) > len(data) {
			return nil, errors.New("bad pkt-line length")
		}
		line := data[4:n]
		data = data[n:]
		line = bytes.TrimSuffix(line, []byte("\n"))
		if bytes.HasPrefix(line, []byte("#")) {
			continue
		}
		if i := bytes.IndexByte(line, 0); i >= 0 {
			line = line[:i] // capabilities follow the first ref
		}
		sha, name, ok := strings.Cut(string(line), " ")
		if !ok || len(sha) != 40 {
			continue
		}
		if base, ok := strings.CutSuffix(name, "^{}"); ok {
			peeled[base] = sha
			continue
		}
		refs[name] = sha
	}
	for name, sha := range peeled {
		refs[name] = sha
	}
	return refs, nil
}

// release returns package p's release in tag t, or nil when the tag's
// folder for the package does not have it.
func (g *GitHub) release(ctx context.Context, t tag, p GitHubPackage) (*Release, error) {
	dir := ""
	if p.PathFor != nil {
		var ok bool
		if dir, ok = p.PathFor(t.version); !ok {
			return nil, nil
		}
	}
	data, ok, err := g.manifest(ctx, t.commit, dir)
	if err != nil {
		return nil, fmt.Errorf("tag %s: %w", t.name, err)
	}
	if !ok {
		return nil, nil
	}
	var m struct {
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		Unity        any               `json:"unity"`
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &m); err != nil || m.Name != p.Name {
		return nil, nil
	}
	if m.Version == "" {
		// Not read with versionFromTag either: the conversion checks the
		// package.json version against UpstreamVersion, whose "" means the
		// same as Version.
		return nil, nil
	}
	v, upstream := t.version, m.Version
	if p.VersionFromTag == nil || !p.VersionFromTag(t.version) {
		if v, err = semver.Parse(m.Version); err != nil {
			return nil, nil
		}
		upstream = ""
	}
	var overlays []Overlay
	if p.OverlaysFor != nil {
		for _, o := range p.OverlaysFor(t.version) {
			owner, repo, err := recipe.GitHubRepo(o.URL)
			if err != nil {
				return nil, fmt.Errorf("tag %s: overlay %s: %w", t.name, o.Path, err)
			}
			overlays = append(overlays, Overlay{
				Path:       o.Path,
				ArchiveURL: fmt.Sprintf("%s/%s/%s/tar.gz/%s", g.CodeloadBase, owner, repo, o.Commit),
				CacheKey:   path.Join("git", "github.com", owner, repo, o.Commit+".tar.gz"),
			})
		}
	}
	unity, _ := m.Unity.(string)
	return &Release{
		Version:         v,
		UpstreamVersion: upstream,
		Unity:           unity,
		Dependencies:    m.Dependencies,
		Dir:             dir,
		ArchiveURL:      fmt.Sprintf("%s/%s/%s/tar.gz/%s", g.CodeloadBase, g.Owner, g.Repo, t.commit),
		Pinned:          true,
		CacheKey:        path.Join("git", "github.com", g.Owner, g.Repo, t.commit+".tar.gz"),
		Origin:          "tag " + t.name,
		Overlays:        overlays,
	}, nil
}

// manifest returns package.json in folder dir of commit, from the cache
// when it is there. A missing file is cached too, as a ".missing" marker.
func (g *GitHub) manifest(ctx context.Context, commit, dir string) ([]byte, bool, error) {
	key := url.PathEscape(dir)
	if key == "" {
		key = "_root"
	}
	base := filepath.Join(g.CacheDir, "git", "github.com", g.Owner, g.Repo, "manifests", commit, key)
	if data, err := os.ReadFile(base + ".json"); err == nil {
		return data, true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	if fsutil.Exists(base + ".missing") {
		return nil, false, nil
	}
	file := "package.json"
	if dir != "" {
		file = dir + "/package.json"
	}
	u := fmt.Sprintf("%s/%s/%s/%s/%s", g.RawBase, g.Owner, g.Repo, commit, file)
	data, ok, err := g.Client.GetBytesIfExists(ctx, u)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, fsutil.WriteFileAtomic(base+".missing", nil, 0o644)
	}
	return data, true, fsutil.WriteFileAtomic(base+".json", data, 0o644)
}
