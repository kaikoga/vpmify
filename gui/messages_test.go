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
			&Message{Items: []Item{{Kind: "noList", Args: map[string]string{"recipe": "kit"}}}, Detail: "kit: no usable version list: none yet"}},
		// Every known error in the tree is said, in its order.
		{"joined", errors.Join(fmt.Errorf("a: %w", &vpmify.NoRecipeError{Recipe: "a"}), fmt.Errorf("kit: %w", noList)),
			&Message{Items: []Item{{Kind: "noRecipe", Args: map[string]string{"recipe": "a"}}, {Kind: "noList", Args: map[string]string{"recipe": "kit"}}},
				Detail: "a: no recipe \"a\"\nkit: no usable version list: none yet"}},
		// Two recipes provide the package a dependency asks for, and
		// neither list has a version for it: updating the first may not
		// help, so the second is said too.
		{"two providers", fmt.Errorf("app 2.0.0 needs pkg >=1.0.0: %w", errors.Join(
			fmt.Errorf("b: %w", &vpmify.NoMatchError{Recipe: "b", Package: "pkg", Range: ">=1.0.0", Dependent: "app 2.0.0"}),
			fmt.Errorf("c: %w", &vpmify.NoMatchError{Recipe: "c", Package: "pkg", Range: ">=1.0.0", Dependent: "app 2.0.0"}))),
			&Message{Items: []Item{
				{Kind: "noMatch", Args: map[string]string{"recipe": "b", "package": "pkg", "range": ">=1.0.0", "dependent": "app 2.0.0"}},
				{Kind: "noMatch", Args: map[string]string{"recipe": "c", "package": "pkg", "range": ">=1.0.0", "dependent": "app 2.0.0"}},
			}, Detail: "app 2.0.0 needs pkg >=1.0.0: b: the version list of pkg has no version that satisfies it\nc: the version list of pkg has no version that satisfies it"}},
		// The sources of one recipe that lack a list say the same line.
		{"said once", errors.Join(&vpmify.NoListError{Recipe: "kit", Source: "a", Reason: "r"}, &vpmify.NoListError{Recipe: "kit", Source: "b", Reason: "r"}),
			&Message{Items: []Item{{Kind: "noList", Args: map[string]string{"recipe": "kit"}}}, Detail: "no usable version list for a: r\nno usable version list for b: r"}},
		// A failure of another kind would be hidden behind the words for
		// the known one.
		{"joined with an unknown", errors.Join(errors.New("GET x: 404"), fmt.Errorf("kit: %w", noList)),
			&Message{Detail: "GET x: 404\nkit: no usable version list: none yet"}},
		{"not listed", fmt.Errorf("pkg: %w", &vpmify.NotListedError{Recipe: "kit", Version: "1.2.3", Package: "pkg"}),
			&Message{Items: []Item{{Kind: "notListed", Args: map[string]string{"recipe": "kit", "version": "1.2.3"}}}, Detail: "pkg: the version list has no 1.2.3: it is outside the package's versions, or the version list is older than it"}},
		{"no recipe", &vpmify.NoRecipeError{Recipe: "kit"},
			&Message{Items: []Item{{Kind: "noRecipe", Args: map[string]string{"recipe": "kit"}}}, Detail: `no recipe "kit"`}},
		// The GUI's own failures are said whole by the front end.
		{"link", linkErr, &Message{Items: []Item{{Kind: "linkNotHttps", Args: map[string]string{"url": "http://example.com/r.json"}}}}},
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
	if jerr != nil || string(got) != `{"items":[{"kind":"noRecipe","args":{"recipe":"kit"}}],"detail":"no recipe \"kit\""}` {
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
