// Package convert turns an upstream archive into a folder that ALCOM accepts
// as a user package: package.json at the root, with the recipe's overrides
// written into it so that ALCOM and Unity read the same manifest.
package convert

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kaikoga/vpmify/internal/fsutil"
	"github.com/kaikoga/vpmify/internal/ojson"
	"github.com/kaikoga/vpmify/internal/recipe"
)

// Result tells what extraction left out.
type Result struct {
	// Skipped lists entries that were not regular files or folders.
	Skipped []string
}

// ExtractTarGz extracts a gzipped tar into dest. It drops the first path
// component of every entry (npm tarballs put everything under "package/",
// GitHub tarballs under "<repo>-<hash>/"), then keeps only the entries under
// subpath, with subpath dropped too.
func ExtractTarGz(archive, dest, subpath string) (Result, error) {
	var res Result
	f, err := os.Open(archive)
	if err != nil {
		return res, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return res, fmt.Errorf("%s: %w", archive, err)
	}
	defer gz.Close()

	prefix := ""
	if subpath != "" {
		clean := path.Clean(strings.Trim(filepath.ToSlash(subpath), "/"))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return res, fmt.Errorf("bad path %q", subpath)
		}
		prefix = clean + "/"
	}

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, fmt.Errorf("%s: %w", archive, err)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		_, rest, ok := strings.Cut(name, "/")
		if !ok || rest == "" {
			continue
		}
		if prefix != "" {
			if !strings.HasPrefix(rest, prefix) {
				continue
			}
			rest = strings.TrimPrefix(rest, prefix)
			if rest == "" {
				continue
			}
		}
		rel := path.Clean(rest)
		if path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
			return res, fmt.Errorf("%s: entry %q escapes the package", archive, hdr.Name)
		}
		// Localize, not FromSlash: on Windows a backslash in the name is also a
		// separator, and the check above sees only slashes.
		local, err := filepath.Localize(rel)
		if err != nil {
			return res, fmt.Errorf("%s: entry %q escapes the package", archive, hdr.Name)
		}
		target := filepath.Join(dest, local)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return res, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return res, err
			}
			mode := os.FileMode(0o644)
			if hdr.FileInfo().Mode()&0o111 != 0 {
				mode = 0o755
			}
			if err := writeFile(target, tr, mode); err != nil {
				return res, err
			}
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
		default:
			res.Skipped = append(res.Skipped, rel)
		}
	}
	return res, nil
}

func writeFile(target string, r io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Manifest is the part of package.json that Vpmify checks.
type Manifest struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Unity   string `json:"unity"`
}

// ReadManifest reads package.json at the root of dir.
func ReadManifest(dir string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return m, err
	}
	// Decode field by field: upstream package.json may hold a "unity" that
	// is not a string, and Vpmify should still read name and version.
	obj, err := ojson.Parse(data)
	if err != nil {
		return m, fmt.Errorf("package.json: %w", err)
	}
	if _, err := obj.GetInto("name", &m.Name); err != nil {
		return m, fmt.Errorf("package.json name: %w", err)
	}
	if _, err := obj.GetInto("version", &m.Version); err != nil {
		return m, fmt.Errorf("package.json version: %w", err)
	}
	_, _ = obj.GetInto("unity", &m.Unity)
	return m, nil
}

// CheckManifest makes sure dir holds the package the source said it would.
func CheckManifest(dir, name, version string) error {
	m, err := ReadManifest(dir)
	if err != nil {
		return err
	}
	if m.Name != name {
		return fmt.Errorf("package.json name is %q, want %q", m.Name, name)
	}
	if m.Version != version {
		return fmt.Errorf("package.json version is %q, want %q", m.Version, version)
	}
	return nil
}

// ApplyOverrides writes the edits into package.json at the root of dir, in
// order, and in each its set before its merge. A null value in set removes
// the field. merge adds keys to a field, which it makes when package.json
// has none or null there, and a null value removes the key; a field that is
// not an object cannot take them.
// Other fields, and the keys already in a field, keep their order.
func ApplyOverrides(dir string, edits []recipe.Edit) error {
	if len(edits) == 0 {
		return nil
	}
	p := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	obj, err := ojson.Parse(data)
	if err != nil {
		return fmt.Errorf("package.json: %w", err)
	}
	for _, e := range edits {
		for _, key := range sortedKeys(e.Set) {
			raw := e.Set[key]
			if string(raw) == "null" {
				obj.Delete(key)
				continue
			}
			obj.Set(key, raw)
		}
		for _, key := range sortedKeys(e.Merge) {
			add := e.Merge[key]
			if len(add) == 0 {
				continue
			}
			field := ojson.New()
			raw, had := obj.Get(key)
			had = had && string(raw) != "null"
			if had {
				if field, err = ojson.Parse(raw); err != nil {
					return fmt.Errorf("package.json %s is not an object, so the recipe cannot merge into it", key)
				}
			}
			for _, k := range sortedKeys(add) {
				if string(add[k]) == "null" {
					field.Delete(k)
					continue
				}
				field.Set(k, add[k])
			}
			// Removing keys from a field that is not there makes no field.
			if !had && len(field.Keys()) == 0 {
				continue
			}
			raw, err := field.MarshalJSON()
			if err != nil {
				return err
			}
			obj.Set(key, raw)
		}
	}
	out, err := obj.Format("  ", ojson.DetectEOL(data), ojson.HasTrailingEOL(data))
	if err != nil {
		return err
	}
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(p, out, info.Mode().Perm())
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Keys new to package.json are appended, so keep their order stable.
	sort.Strings(keys)
	return keys
}
