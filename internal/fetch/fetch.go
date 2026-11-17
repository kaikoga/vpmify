// Package fetch downloads metadata and archives over HTTP(S).
package fetch

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client is an HTTP client with Vpmify's defaults.
type Client struct {
	HTTP      *http.Client
	UserAgent string
}

// NewClient returns a client with a generous timeout for large archives.
func NewClient(userAgent string) *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 10 * time.Minute},
		UserAgent: userAgent,
	}
}

func (c *Client) get(ctx context.Context, url, accept string) (*http.Response, error) {
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
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

// GetJSON decodes the JSON at url into dst.
func (c *Client) GetJSON(ctx context.Context, url string, dst any) error {
	resp, err := c.get(ctx, url, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	return nil
}

// GetBytes returns the body at url.
func (c *Client) GetBytes(ctx context.Context, url string) ([]byte, error) {
	resp, err := c.get(ctx, url, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// GetBytesIfExists returns the body at url, or found=false on 404.
func (c *Client) GetBytesIfExists(ctx context.Context, url string) (data []byte, found bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		data, err := io.ReadAll(resp.Body)
		return data, err == nil, err
	case http.StatusNotFound:
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("GET %s: %s", url, resp.Status)
}

// Download saves url to dest, checking it against the digests first. It
// writes to a temporary file next to dest and renames it, so a broken
// download never sits at dest.
func (c *Client) Download(ctx context.Context, url, dest string, digests Digests) error {
	resp, err := c.get(ctx, url, "")
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
