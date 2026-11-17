// Package testutil builds fixtures for tests.
package testutil

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"sort"
)

// Entry is one entry of a tarball. Body is ignored for non-regular entries.
type Entry struct {
	Name     string
	Body     string
	Typeflag byte
	Linkname string
	Mode     int64
}

// TarGz builds a gzipped tar from files (path to content), in sorted order.
func TarGz(files map[string]string) []byte {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	entries := make([]Entry, 0, len(names))
	for _, n := range names {
		entries = append(entries, Entry{Name: n, Body: files[n]})
	}
	return TarGzEntries(entries)
}

// TarGzEntries builds a gzipped tar from entries, in order.
func TarGzEntries(entries []Entry) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.Typeflag
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.Mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Name: e.Name, Typeflag: typ, Mode: mode, Linkname: e.Linkname}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.Body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			panic(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				panic(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		panic(err)
	}
	if err := gz.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// Integrity returns the npm "sha512-<base64>" string of data.
func Integrity(data []byte) string {
	sum := sha512.Sum512(data)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}
