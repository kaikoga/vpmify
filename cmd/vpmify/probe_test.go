package main

import (
	"bytes"
	"testing"

	"github.com/kaikoga/vpmify/internal/app"
	"github.com/kaikoga/vpmify/internal/source"
)

// The words recipe probe prints: the versions each package takes, and
// what the source leaves out by reason, naming the package only when the
// source has several.
func TestPrintProbe(t *testing.T) {
	res := &app.ProbeResult{Recipe: "mono", Sources: []app.ProbeSource{
		{
			Source: "https://github.com/owner/mono.git",
			Packages: []app.ProbePackage{
				{Package: "com.example.base", Versions: []app.ProbeVersion{{Version: "1.0.0", Tag: "v1.0.0"}, {Version: "1.1.0", Tag: "1.1.0"}}},
				{Package: "com.example.pkg", Versions: []app.ProbeVersion{}},
			},
			Skipped: []source.Skip{
				{Tag: "latest", Reason: "the name is not a version"},
				{Tag: "v0.9.0", Version: "0.9.0", Reason: "outside the source's tags"},
				{Tag: "v2.0.0", Package: "com.example.base", Version: "2.0.0", Reason: "no package.json in Packages/Base"},
				{Tag: "nightly", Reason: "the name is not a version"},
			},
			Warnings: []string{"tags v1 and v1.0 read as 1.0.0"},
		},
		{Source: "npm https://registry.npmjs.org", Error: "GET x: 404"},
	}}
	var out bytes.Buffer
	if err := printProbe(&out, res); err != nil {
		t.Fatal(err)
	}
	want := `mono: https://github.com/owner/mono.git
  com.example.base: takes 2: 1.0.0 (tag v1.0.0), 1.1.0
  com.example.pkg: takes no version
  warning: tags v1 and v1.0 read as 1.0.0
  leaves out:
    the name is not a version: latest, nightly
    outside the source's tags: v0.9.0 (0.9.0)
    no package.json in Packages/Base: v2.0.0 (2.0.0) for com.example.base

mono: npm https://registry.npmjs.org
  cannot be read: GET x: 404
`
	if got := out.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
