package main

import (
	"reflect"
	"testing"
)

func TestRecipeURLs(t *testing.T) {
	tests := []struct {
		link string
		want []string // nil means an error
	}{
		{"vpmify://recipe/add?url=https://example.com/kit.json", []string{"https://example.com/kit.json"}},
		{"vpmify://recipe/add/?url=https%3A%2F%2Fexample.com%2Fkit.json%3Fv%3D1", []string{"https://example.com/kit.json?v=1"}},
		{"vpmify://recipe/add?url=https://example.com/a.json&url=https://example.com/b.json", []string{"https://example.com/a.json", "https://example.com/b.json"}},
		{"vpmify://recipe/add?url=http://example.com/kit.json", nil},
		{"vpmify://recipe/add?url=HTTPS://x/../../../../etc/passwd", nil},
		{"vpmify://recipe/add?url=Https://example.com/kit.json", nil},
		{"vpmify://recipe/add?url=file:///etc/passwd", nil},
		{"vpmify://recipe/add?url=/Users/someone/kit.json", nil},
		{"vpmify://recipe/add?url=https://example.com/a.json&url=file:///etc/passwd", nil},
		{"vpmify://recipe/add", nil},
		{"vpmify://recipe/remove?url=https://example.com/kit.json", nil},
		{"vpmify://recipe/add/more?url=https://example.com/kit.json", nil},
		{"vpmify://recipe?url=https://example.com/kit.json", nil},
		{"vpmify://package/add?url=https://example.com/kit.json", nil},
		// The form before recipe/add, which was never published.
		{"vpmify://add?url=https://example.com/kit.json", nil},
		{"https://example.com/kit.json", nil},
	}
	for _, tt := range tests {
		got, err := recipeURLs(tt.link)
		if tt.want == nil {
			if err == nil {
				t.Errorf("%s: got %v, want an error", tt.link, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %v, %v, want %v", tt.link, got, err, tt.want)
		}
	}
}
