package fetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// roundTripper answers requests through the client's transport, so tests
// open no port.
type roundTripper func(w http.ResponseWriter, req *http.Request)

func (f roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	f(rec, req)
	return rec.Result(), nil
}

func newTestClient(serve roundTripper) *Client {
	c := NewClient("vpmify-test")
	c.HTTP.Transport = serve
	return c
}

func TestReadsOnlyHTTPS(t *testing.T) {
	asked := 0
	c := newTestClient(func(w http.ResponseWriter, req *http.Request) {
		asked++
		_, _ = w.Write([]byte("body"))
	})
	ctx := context.Background()
	if _, err := c.GetBytes(ctx, "http://example.test/a", 100); err == nil || !strings.Contains(err.Error(), "only https") {
		t.Errorf("GetBytes: err = %v", err)
	}
	if _, _, err := c.GetBytesIfExists(ctx, "http://example.test/a"); err == nil || !strings.Contains(err.Error(), "only https") {
		t.Errorf("GetBytesIfExists: err = %v", err)
	}
	if asked != 0 {
		t.Errorf("asked = %d, want 0", asked)
	}
	if data, err := c.GetBytes(ctx, "https://example.test/a", 100); err != nil || string(data) != "body" {
		t.Errorf("https: data = %q, err = %v", data, err)
	}
}

func TestFollowsRedirectsOnlyToHTTPS(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/to-https":
			http.Redirect(w, req, "https://example.test/body", http.StatusFound)
		case "/to-http":
			http.Redirect(w, req, "http://example.test/body", http.StatusFound)
		case "/body":
			_, _ = w.Write([]byte("body"))
		}
	})
	ctx := context.Background()
	if data, err := c.GetBytes(ctx, "https://example.test/to-https", 100); err != nil || string(data) != "body" {
		t.Errorf("to https: data = %q, err = %v", data, err)
	}
	if _, err := c.GetBytes(ctx, "https://example.test/to-http", 100); err == nil || !strings.Contains(err.Error(), "refused a redirect") {
		t.Errorf("to http: err = %v", err)
	}
}

// sized serves a body of n bytes, saying its length when known is true.
func sized(n int, known bool) roundTripper {
	return func(w http.ResponseWriter, req *http.Request) {
		if known {
			w.Header().Set("Content-Length", strconv.Itoa(n))
		}
		_, _ = w.Write([]byte(strings.Repeat("x", n)))
	}
}

func TestRefusesBodiesOverTheLimit(t *testing.T) {
	ctx := context.Background()
	for _, known := range []bool{true, false} {
		c := newTestClient(sized(10, known))
		if data, err := c.GetBytes(ctx, "https://example.test/a", 10); err != nil || len(data) != 10 {
			t.Errorf("known=%v, at the limit: len = %d, err = %v", known, len(data), err)
		}
		if _, err := c.GetBytes(ctx, "https://example.test/a", 9); !errors.Is(err, ErrTooLarge) {
			t.Errorf("known=%v, GetBytes: err = %v", known, err)
		}
		c.MaxMetadata = 9
		if _, _, err := c.GetBytesIfExists(ctx, "https://example.test/a"); !errors.Is(err, ErrTooLarge) {
			t.Errorf("known=%v, GetBytesIfExists: err = %v", known, err)
		}
	}
}

func TestGetJSONStopsAtTheLimit(t *testing.T) {
	c := newTestClient(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.WriteString(w, `{"name": "`+strings.Repeat("x", 100)+`"}`)
	})
	c.MaxMetadata = 50
	var doc struct{ Name string }
	if err := c.GetJSON(context.Background(), "https://example.test/a", &doc); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v", err)
	}
}

func TestDownloadStopsAtTheLimit(t *testing.T) {
	for _, known := range []bool{true, false} {
		c := newTestClient(sized(10, known))
		c.MaxArchive = 9
		dir := t.TempDir()
		dest := filepath.Join(dir, "a.tgz")
		if err := c.Download(context.Background(), "https://example.test/a.tgz", dest, Digests{}); !errors.Is(err, ErrTooLarge) {
			t.Errorf("known=%v: err = %v", known, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("known=%v: left %v", known, entries)
		}
	}
}
