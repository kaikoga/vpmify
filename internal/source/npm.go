package source

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/semver"
)

// NPM is packages on an npm-compatible registry (npmjs, OpenUPM).
type NPM struct {
	Registry string
	Packages []string
	Client   *fetch.Client
}

// packument is the part of the registry's package document Vpmify reads.
// It holds the package.json of every version, so one request is enough.
type packument struct {
	Name     string                     `json:"name"`
	Versions map[string]packumentRecord `json:"versions"`
}

type packumentRecord struct {
	Version      string            `json:"version"`
	Unity        any               `json:"unity"`
	Dependencies map[string]string `json:"dependencies"`
	Dist         struct {
		Tarball   string `json:"tarball"`
		Integrity string `json:"integrity"`
		Shasum    string `json:"shasum"`
	} `json:"dist"`
}

// registryKey names a registry in the cache, so that recipes reading one
// package from two registries (npmjs and OpenUPM) do not share archives,
// which may differ. It is one folder name that every OS takes.
func registryKey(registry string) string {
	key := strings.TrimRight(registry, "/")
	if u, err := url.Parse(key); err == nil && u.Host != "" {
		key = u.Host + u.Path
	}
	return strings.NewReplacer(":", "_", "/", "_", `\`, "_").Replace(key)
}

// Releases lists the versions of each package in the registry, oldest
// first. Versions whose number is not SemVer are left out.
func (n *NPM) Releases(ctx context.Context) (map[string][]Release, error) {
	out := map[string][]Release{}
	for _, name := range n.Packages {
		rels, err := n.releases(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = rels
	}
	return out, nil
}

func (n *NPM) releases(ctx context.Context, name string) ([]Release, error) {
	u := strings.TrimRight(n.Registry, "/") + "/" + url.PathEscape(name)
	var doc packument
	if err := n.Client.GetJSON(ctx, u, &doc); err != nil {
		return nil, err
	}
	if doc.Name != name {
		return nil, fmt.Errorf("registry returned %q for %q", doc.Name, name)
	}
	var out []Release
	for key, rec := range doc.Versions {
		v, err := semver.Parse(key)
		if err != nil {
			continue
		}
		if rec.Dist.Tarball == "" {
			continue
		}
		unity, _ := rec.Unity.(string)
		out = append(out, Release{
			Version:      v,
			Unity:        unity,
			Dependencies: rec.Dependencies,
			ArchiveURL:   rec.Dist.Tarball,
			Digests:      fetch.Digests{Integrity: rec.Dist.Integrity, Shasum: rec.Dist.Shasum},
			CacheKey:     path.Join("npm", registryKey(n.Registry), name, v.String()+".tgz"),
			Origin:       "npm " + v.String(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return semver.Compare(out[i].Version, out[j].Version) < 0 })
	return out, nil
}
