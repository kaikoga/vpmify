package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kaikoga/vpmify/internal/paths"
)

// withData points Vpmify's data at a new folder, with ALCOM's settings.json
// when settings is true, and returns the folder.
func withData(t *testing.T, settings bool) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", base)
	t.Setenv("LOCALAPPDATA", base)
	if settings {
		p := paths.Layout{Base: base}.ALCOMSettings()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"userPackageFolders": []}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

// runJSON runs vpmify with args and returns the exit code and the one JSON
// object it printed on stdout.
func runJSON(t *testing.T, args ...string) (int, map[string]any) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	dec := json.NewDecoder(&stdout)
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		t.Fatalf("%v: stdout is not a JSON object: %v\nstdout: %s\nstderr: %s", args, err, stdout.String(), stderr.String())
	}
	if dec.More() {
		t.Errorf("%v: stdout has more than one JSON value", args)
	}
	return code, obj
}

func TestJSONSuccess(t *testing.T) {
	withData(t, true)
	tests := []struct {
		args []string
		want map[string]any
	}{
		{[]string{"version", "--json"}, map[string]any{"version": "dev"}},
		{[]string{"package", "list", "--json"}, map[string]any{"entries": []any{}}},
		{[]string{"--json", "package", "list"}, map[string]any{"entries": []any{}}},
		{[]string{"help", "--json"}, map[string]any{"usage": usage}},
		{[]string{"recipe", "list", "--json"}, map[string]any{"recipes": []any{}}},
		{[]string{"recipe", "update", "--json"}, map[string]any{"recipes": []any{}, "errors": []any{}}},
		{[]string{"package", "pull", "--json"}, map[string]any{
			"built": []any{}, "upToDate": []any{}, "registered": []any{}, "unregistered": []any{}, "errors": []any{},
		}},
		{[]string{"clean", "--json"}, map[string]any{"applied": false, "entries": []any{}}},
		{[]string{"doctor", "--json"}, map[string]any{"problems": []any{}, "ok": true}},
	}
	for _, tt := range tests {
		code, obj := runJSON(t, tt.args...)
		if code != 0 || !reflect.DeepEqual(obj, tt.want) {
			t.Errorf("%v = %d %v, want 0 %v", tt.args, code, obj, tt.want)
		}
	}
}

func TestJSONErrors(t *testing.T) {
	withData(t, false)
	tests := []struct {
		args []string
		code int
	}{
		// A command that does nothing prints only "error".
		{[]string{"recipe", "versions", "--json", "nope"}, 1},
		{[]string{"recipe", "remove", "--json"}, 2},
		{[]string{"recipe", "remove", "--json", "nope"}, 1},
		{[]string{"recipe", "remove", "--json", "nope/com.example.pkg"}, 1},
		{[]string{"recipe", "--json"}, 2},
		{[]string{"recipe", "nope", "--json"}, 2},
		{[]string{"package", "list", "--json", "nope"}, 1},
		// An id that is not one names no file, such as state.json.
		{[]string{"package", "list", "--json", "../state"}, 1},
		// package remove always names a version or --all.
		{[]string{"package", "remove", "--json", "nope"}, 2},
		{[]string{"package", "remove", "--json", "nope@"}, 2},
		{[]string{"package", "remove", "--json", "--all", "nope@1.0.0"}, 2},
		{[]string{"package", "remove", "--json", "nope@1.0.0"}, 1},
		{[]string{"package", "remove", "--json", "--all", "nope"}, 1},
		{[]string{"package", "--json"}, 2},
		{[]string{"nope", "--json"}, 2},
		{[]string{"--json"}, 2},
		// recipe add reads every argument as a recipe, even one that starts
		// with "-".
		{[]string{"recipe", "add", "--json", "-missing.json"}, 1},
	}
	for _, tt := range tests {
		code, obj := runJSON(t, tt.args...)
		if msg, _ := obj["error"].(string); code != tt.code || len(obj) != 1 || msg == "" {
			t.Errorf("%v = %d %v, want %d and only an error", tt.args, code, obj, tt.code)
		}
	}
}

// A command that did part of its work prints what it did and "error".
func TestJSONPartialFailure(t *testing.T) {
	withData(t, false)
	code, obj := runJSON(t, "package", "pull", "--json", "nope")
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	msg, _ := obj["error"].(string)
	errs, _ := obj["errors"].([]any)
	if msg == "" || len(errs) == 0 || obj["built"] == nil {
		t.Errorf("pull = %v", obj)
	}
	first, _ := errs[0].(map[string]any)
	if first["recipe"] != "nope" || !strings.Contains(msg, "nope: ") {
		t.Errorf("errors = %v, error = %q", errs, msg)
	}
}

func TestJSONListSettingsError(t *testing.T) {
	withData(t, false)
	code, obj := runJSON(t, "package", "list", "--json")
	if msg, _ := obj["settingsError"].(string); code != 0 || msg == "" {
		t.Errorf("list = %d %v, want 0 and a settingsError", code, obj)
	}
}

// A command from before the namespaces fails and names where it went.
func TestMovedCommand(t *testing.T) {
	withData(t, false)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"pull"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "it is now `vpmify package pull`") {
		t.Errorf("pull = %d, stderr = %s", code, stderr.String())
	}
}
