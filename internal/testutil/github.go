package testutil

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
)

// FakeGitHub answers GitHub's smart HTTP ref advertisement, raw file and
// codeload tarball requests for one repository, through an
// http.RoundTripper, so tests open no port.
type FakeGitHub struct {
	Owner, Repo string

	mu   sync.Mutex
	tags map[string]string            // tag -> commit
	tree map[string]map[string]string // commit -> path -> content
	// Requests counts requests by kind: "refs", "raw", "codeload".
	Requests map[string]int
}

// NewFakeGitHub returns an empty repository.
func NewFakeGitHub(owner, repo string) *FakeGitHub {
	return &FakeGitHub{
		Owner: owner, Repo: repo,
		tags:     map[string]string{},
		tree:     map[string]map[string]string{},
		Requests: map[string]int{},
	}
}

// Tag adds a tag pointing at a new commit with the given files.
func (g *FakeGitHub) Tag(name string, files map[string]string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	sum := sha1.Sum([]byte(name))
	commit := hex.EncodeToString(sum[:])
	g.tags[name] = commit
	g.tree[commit] = files
	return commit
}

func pkt(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }

// RoundTrip implements http.RoundTripper.
func (g *FakeGitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec := httptest.NewRecorder()
	prefix := "/" + g.Owner + "/" + g.Repo
	switch req.URL.Host {
	case "github.com":
		if req.URL.Path == prefix+".git/info/refs" && req.URL.Query().Get("service") == "git-upload-pack" {
			g.Requests["refs"]++
			var b strings.Builder
			b.WriteString(pkt("# service=git-upload-pack\n"))
			b.WriteString("0000")
			names := make([]string, 0, len(g.tags))
			for n := range g.tags {
				names = append(names, n)
			}
			sort.Strings(names)
			b.WriteString(pkt("0000000000000000000000000000000000000000 HEAD\x00multi_ack side-band-64k\n"))
			for _, n := range names {
				b.WriteString(pkt(g.tags[n] + " refs/tags/" + n + "\n"))
			}
			b.WriteString("0000")
			_, _ = rec.WriteString(b.String())
			return rec.Result(), nil
		}
	case "raw.githubusercontent.com":
		if rest, ok := strings.CutPrefix(req.URL.Path, prefix+"/"); ok {
			g.Requests["raw"]++
			commit, file, _ := strings.Cut(rest, "/")
			if content, ok := g.tree[commit][file]; ok {
				_, _ = rec.WriteString(content)
				return rec.Result(), nil
			}
		}
	case "codeload.github.com":
		if commit, ok := strings.CutPrefix(req.URL.Path, prefix+"/tar.gz/"); ok {
			if files, ok := g.tree[commit]; ok {
				g.Requests["codeload"]++
				top := g.Repo + "-" + commit[:7] + "/"
				prefixed := map[string]string{}
				for p, c := range files {
					prefixed[top+p] = c
				}
				_, _ = rec.Write(TarGz(prefixed))
				return rec.Result(), nil
			}
		}
	}
	http.NotFound(rec, req)
	return rec.Result(), nil
}
