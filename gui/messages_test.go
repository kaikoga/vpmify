package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	vpmify "github.com/kaikoga/vpmify/internal/app"
)

func TestMessageOf(t *testing.T) {
	noList := &vpmify.NoListError{Recipe: "kit", Reason: "none yet"}
	_, linkErr := recipeURLs("vpmify://recipe/add?url=http://example.com/r.json")
	tests := []struct {
		name string
		err  error
		want *Message
	}{
		{"none", nil, nil},
		{"wrapped", fmt.Errorf("kit: %w", noList),
			&Message{Kind: "noList", Args: map[string]string{"recipe": "kit"}, Detail: "kit: no usable version list: none yet"}},
		// The first known error in the tree names the kind, whatever its
		// type.
		{"joined", errors.Join(fmt.Errorf("a: %w", &vpmify.NoRecipeError{Recipe: "a"}), fmt.Errorf("kit: %w", noList)),
			&Message{Kind: "noRecipe", Args: map[string]string{"recipe": "a"}, Detail: "a: no recipe \"a\"\nkit: no usable version list: none yet"}},
		// A failure of another kind would be hidden behind the words for
		// the known one.
		{"joined with an unknown", errors.Join(errors.New("GET x: 404"), fmt.Errorf("kit: %w", noList)),
			&Message{Detail: "GET x: 404\nkit: no usable version list: none yet"}},
		{"not listed", fmt.Errorf("pkg: %w", &vpmify.NotListedError{Recipe: "kit", Version: "1.2.3", Package: "pkg"}),
			&Message{Kind: "notListed", Args: map[string]string{"recipe": "kit", "version": "1.2.3"}, Detail: "pkg: the version list has no 1.2.3: it is outside the package's versions, or the version list is older than it"}},
		{"no match", &vpmify.NoMatchError{Recipe: "kit", Package: "pkg", Range: ">=1.0.0"},
			&Message{Kind: "noMatch", Args: map[string]string{"recipe": "kit", "package": "pkg", "range": ">=1.0.0"}, Detail: "the version list of pkg has no version that satisfies it"}},
		{"no recipe", &vpmify.NoRecipeError{Recipe: "kit"},
			&Message{Kind: "noRecipe", Args: map[string]string{"recipe": "kit"}, Detail: `no recipe "kit"`}},
		// The GUI's own failures are said whole by the front end.
		{"link", linkErr, &Message{Kind: "linkNotHttps", Args: map[string]string{"url": "http://example.com/r.json"}}},
		{"unknown", errors.New("GET x: 404"), &Message{Detail: "GET x: 404"}},
	}
	for _, tt := range tests {
		if got := messageOf(tt.err); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

// Wails marshals an error a method returns with json.Marshal(&err), and the
// front end gets it as the cause of the failed call.
func TestDescribeMarshalsTheMessage(t *testing.T) {
	err := describe(&vpmify.NoRecipeError{Recipe: "kit"})
	got, jerr := json.Marshal(&err)
	if jerr != nil || string(got) != `{"kind":"noRecipe","args":{"recipe":"kit"},"detail":"no recipe \"kit\""}` {
		t.Errorf("marshaled = %s, %v", got, jerr)
	}
	if err.Error() != `no recipe "kit"` {
		t.Errorf("message = %q", err)
	}
	plain := errors.New("GET x: 404")
	if describe(plain) != plain || describe(nil) != nil {
		t.Error("an error without a kind is not returned as it is")
	}
}
