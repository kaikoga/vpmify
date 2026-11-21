// Package fetch downloads metadata and archives over https.
package fetch

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The limits NewClient sets. They are far above what real packages need,
// so that only an upstream that means harm, or is broken, reaches them.
const (
	// DefaultMaxMetadata limits the files read to make version lists, such
	// as an npm registry's document of a package.
	DefaultMaxMetadata = 64 << 20
	// DefaultMaxArchive limits one archive. A package that ships machine
	// learning data can pass 1 GB.
	DefaultMaxArchive = 8 << 30
)

// Client is an HTTP client with Vpmify's defaults. It reads only https
// URLs, and follows redirects only to https URLs.
type Client struct {
	HTTP      *http.Client
	UserAgent string
	// MaxMetadata is the most bytes GetJSON and GetBytesIfExists read. A
	// caller of GetBytes that reads such a file passes it too.
	MaxMetadata int64
	// MaxArchive is the most bytes Download saves.
	MaxArchive int64
}

// NewClient returns a client with a generous timeout for large archives.
func NewClient(userAgent string) *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 10 * time.Minute, CheckRedirect: checkRedirect},
		UserAgent:   userAgent,
		MaxMetadata: DefaultMaxMetadata,
		MaxArchive:  DefaultMaxArchive,
	}
}

// checkRedirect keeps net/http's limit of ten redirects and refuses one to
// another scheme than https, which would let anyone on the way change what
// is read.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refused a redirect to %s: only https is read", req.URL.Redacted())
	}
	return nil
}

// ErrTooLarge is wrapped by the errors of a body longer than its limit.
var ErrTooLarge = errors.New("too large")

// limitReader passes on at most max bytes of r, and fails with ErrTooLarge
// when r has more, on that read and every later one. Nothing past max is
// passed on, so a reader that stops at a complete value, as a JSON decoder
// does, cannot succeed on a body over the limit.
type limitReader struct {
	r    io.Reader
	max  int64
	read int64
	err  error
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.err != nil {
		return 0, l.err
	}
	if l.read >= l.max {
		var one [1]byte
		n, err := l.r.Read(one[:])
		if n > 0 {
			l.err = fmt.Errorf("%w: more than %d bytes", ErrTooLarge, l.max)
			return 0, l.err
		}
		return 0, err
	}
	if int64(len(p)) > l.max-l.read {
		p = p[:l.max-l.read]
	}
	n, err := l.r.Read(p)
	l.read += int64(n)
	return n, err
}

// do sends a GET to url, which must be https. A response that says it is
// longer than max is refused before its body is read.
func (c *Client) do(ctx context.Context, url, accept string, max int64) (*http.Response, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("GET %s: only https URLs are read", url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK && resp.ContentLength > max {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %w: %d bytes, more than %d", url, ErrTooLarge, resp.ContentLength, max)
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{&limitReader{r: resp.Body, max: max}, resp.Body}
	return resp, nil
}

func (c *Client) get(ctx context.Context, url, accept string, max int64) (*http.Response, error) {
	resp, err := c.do(ctx, url, accept, max)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

// GetJSON decodes the JSON at url into dst.
func (c *Client) GetJSON(ctx context.Context, url string, dst any) error {
	resp, err := c.get(ctx, url, "application/json", c.MaxMetadata)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	return nil
}

// GetBytes returns the body at url, which may be at most max bytes long.
func (c *Client) GetBytes(ctx context.Context, url string, max int64) ([]byte, error) {
	resp, err := c.get(ctx, url, "", max)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	return data, nil
}

// GetBytesIfExists returns the body at url, or found=false on 404.
func (c *Client) GetBytesIfExists(ctx context.Context, url string) (data []byte, found bool, err error) {
	resp, err := c.do(ctx, url, "", c.MaxMetadata)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, false, fmt.Errorf("GET %s: %w", url, err)
		}
		return data, true, nil
	case http.StatusNotFound:
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("GET %s: %s", url, resp.Status)
}

// Download saves url to dest, checking it against the digests first. It
// writes to a temporary file next to dest and renames it, so a broken
// download never sits at dest.
func (c *Client) Download(ctx context.Context, url, dest string, digests Digests) error {
	resp, err := c.get(ctx, url, "", c.MaxArchive)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("GET %s: %w", url, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := digests.CheckFile(tmpName); err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	return os.Rename(tmpName, dest)
}

// Digests are the expected hashes of a download, as npm publishes them.
type Digests struct {
	// Integrity is a Subresource Integrity string such as "sha512-<base64>".
	// Several space-separated entries mean any of them may match.
	Integrity string
	// Shasum is a hex SHA-1, the older npm field.
	Shasum string
}

// Empty reports whether there is nothing to check against.
func (d Digests) Empty() bool { return d.Integrity == "" && d.Shasum == "" }

// CheckFile checks the file at path against the digests. With no digests it
// accepts the file.
func (d Digests) CheckFile(path string) error {
	if d.Empty() {
		return nil
	}
	if d.Integrity != "" {
		checked := false
		for _, entry := range strings.Fields(d.Integrity) {
			algo, want, ok := strings.Cut(entry, "-")
			if !ok {
				continue
			}
			var h hash.Hash
			switch algo {
			case "sha512":
				h = sha512.New()
			case "sha384":
				h = sha512.New384()
			case "sha256":
				h = sha256.New()
			default:
				continue
			}
			sum, err := hashFile(path, h)
			if err != nil {
				return err
			}
			if base64.StdEncoding.EncodeToString(sum) == want {
				return nil
			}
			checked = true
		}
		if checked {
			return fmt.Errorf("integrity mismatch")
		}
	}
	if d.Shasum != "" {
		sum, err := hashFile(path, sha1.New())
		if err != nil {
			return err
		}
		if strings.EqualFold(hex.EncodeToString(sum), d.Shasum) {
			return nil
		}
		return fmt.Errorf("shasum mismatch")
	}
	return fmt.Errorf("no supported integrity algorithm in %q", d.Integrity)
}

func hashFile(path string, h hash.Hash) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}
