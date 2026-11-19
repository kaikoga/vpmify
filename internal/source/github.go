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
	TagPattern  string
	// TagVersion reads the version of a tag from its name; false means the
	// tag is not read.
	TagVersion func(name string) (semver.Version, bool, error)
	// UniqueTagVersions makes two tags that read as one version an error,
	// for a recipe's own tagVersion rule, which should tell its tags apart.
	UniqueTagVersions bool
	AcceptTag         func(semver.Version) bool
	Client            *fetch.Client
	// CacheDir keeps each commit's package.json, which never changes.
	CacheDir string

	// Endpoints, replaceable in tests.
	GitBase, RawBase, CodeloadBase string
}

// GitHubPackage is one package of the repository.
type GitHubPackage struct {
	// Name is the name package.json must have.
	Name string
	// PathFor returns the folder of the package in a tag, from the version
	// in the tag's name; false means the tag is not read.
	PathFor func(semver.Version) (string, bool)
	// VersionFromTag gives the package the version of the tag, in place of
	// the one in its package.json.
	VersionFromTag bool
}

// NewGitHub returns the upstream of a github recipe source.
func NewGitHub(s *recipe.Source, client *fetch.Client, cacheDir string) (*GitHub, error) {
	owner, repo, err := parseGitHubURL(s.URL)
	if err != nil {
		return nil, err
	}
	pattern := s.TagPattern
	if pattern == "" {
		pattern = "*"
	}
	var pkgs []GitHubPackage
	for _, p := range s.Packages {
		pkgs = append(pkgs, GitHubPackage{Name: p.Name, PathFor: p.PathFor, VersionFromTag: p.VersionFromTag})
	}
	return &GitHub{
		Owner: owner, Repo: repo,
		Packages:          pkgs,
		TagPattern:        pattern,
		TagVersion:        s.TagVersionOf,
		UniqueTagVersions: s.HasTagVersionRule(),
		AcceptTag:         s.AcceptsTag,
		Client:            client,
		CacheDir:          cacheDir,
		GitBase:           "https://github.com",
		RawBase:           "https://raw.githubusercontent.com",
		CodeloadBase:      "https://codeload.github.com",
	}, nil
}

func parseGitHubURL(raw string) (owner, repo string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	if u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") {
		return "", "", fmt.Errorf("github source takes https://github.com/<owner>/<repo> only, not %q", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("github url %q is not https://github.com/<owner>/<repo>", raw)
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
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
func (g *GitHub) Releases(ctx context.Context) (map[string][]Release, error) {
	tags, err := g.tags(ctx)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	out := map[string][]Release{}
	for j, p := range g.Packages {
		// chosen is the tag each version is taken from, by version.
		chosen := map[string]int{}
		var order []string
		for i := range tags { // oldest first
			rel := found[i*n+j]
			if rel == nil {
				continue
			}
			v := rel.Version.String()
			cur, ok := chosen[v]
			switch {
			case !ok:
				chosen[v] = i
				order = append(order, v)
			case !rel.Version.IsPrerelease() && tags[cur].version.IsPrerelease() && !tags[i].version.IsPrerelease():
				chosen[v] = i
			}
		}
		rels := []Release{}
		for _, v := range order {
			i := chosen[v]
			rel := found[i*n+j]
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
	return out, nil
}

// tags lists the tags that match the pattern and whose name reads as a
// version the recipe accepts, oldest first.
func (g *GitHub) tags(ctx context.Context) ([]tag, error) {
	readVersion := g.TagVersion
	if readVersion == nil {
		readVersion = func(name string) (semver.Version, bool, error) {
			v, err := semver.Parse(strings.TrimPrefix(strings.TrimPrefix(name, "v"), "V"))
			return v, err == nil, nil
		}
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
		if ok, _ := path.Match(g.TagPattern, tagName); !ok {
			continue
		}
		v, ok, err := readVersion(tagName)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if g.AcceptTag != nil && !g.AcceptTag(v) {
			continue
		}
		out = append(out, tag{name: tagName, version: v, commit: commit})
	}
	sort.Slice(out, func(i, j int) bool {
		if c := semver.Compare(out[i].version, out[j].version); c != 0 {
			return c < 0
		}
		return out[i].name < out[j].name
	})
	if g.UniqueTagVersions {
		for i := 1; i < len(out); i++ {
			if semver.Compare(out[i-1].version, out[i].version) == 0 {
				return nil, fmt.Errorf("tags %s and %s both read as version %s; change tagVersion to tell them apart", out[i-1].name, out[i].name, out[i].version)
			}
		}
	}
	return out, nil
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
	v, upstream := t.version, m.Version
	if !p.VersionFromTag {
		if v, err = semver.Parse(m.Version); err != nil {
			return nil, nil
		}
		upstream = ""
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
