package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A state file of format 1 is read with its folders and "unity" dropped,
// and saved as the current format.
func TestLoadFormat1(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	v1 := `{
  "format": 1,
  "recipes": {
    "kit": {
      "origin": "https://example.com/kit.json",
      "packages": {
        "com.example.kit": {
          "versions": {
            "1.0.0": {"path": "/old/place/kit/com.example.kit/1.0.0", "fingerprint": "abc", "unity": "2021.3"}
          }
        }
      }
    }
  }
}`
	if err := os.WriteFile(p, []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Recipes["kit"]
	if s.Format != Format || r.Origin != "https://example.com/kit.json" || r.Packages["com.example.kit"].Versions["1.0.0"].Fingerprint != "abc" {
		t.Errorf("loaded %+v", s)
	}
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(data); strings.Contains(text, "path") || strings.Contains(text, "unity") || !strings.Contains(text, `"format": 2`) {
		t.Errorf("saved %s", text)
	}
}

func TestLoadUnknownFormat(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(p, []byte(`{"format": 3, "recipes": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "unknown format 3") {
		t.Errorf("err = %v", err)
	}
}
