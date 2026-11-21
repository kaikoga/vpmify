package main

import (
	"fmt"
	"net/url"
	"strings"
)

// scheme is the custom URL scheme that web pages use to hand a recipe to
// Vpmify with one click. build/config.yml registers the same name.
const scheme = "vpmify"

// schemeURLs picks the vpmify: links out of command line arguments.
func schemeURLs(args []string) []string {
	var urls []string
	for _, a := range args {
		if strings.HasPrefix(a, scheme+":") {
			urls = append(urls, a)
		}
	}
	return urls
}

// recipeURLs reads the recipe URLs out of a link such as
// vpmify://add?url=https://example.com/recipe.json. A link may name several
// recipes with more url parameters. Only https is taken, so that a web page
// cannot make Vpmify read a file on the user's machine.
func recipeURLs(link string) ([]string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return nil, err
	}
	if u.Scheme != scheme || u.Host != "add" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("not a link Vpmify knows: %s", link)
	}
	refs := u.Query()["url"]
	if len(refs) == 0 {
		return nil, fmt.Errorf("the link names no recipe: %s", link)
	}
	for _, ref := range refs {
		// The prefix is checked as well as the scheme: url.Parse lowercases
		// the scheme, but the core takes a reference as a URL only by a
		// lowercase prefix and reads anything else as a file.
		r, err := url.Parse(ref)
		if err != nil || r.Scheme != "https" || r.Host == "" || !strings.HasPrefix(ref, "https://") {
			return nil, fmt.Errorf("the link names a recipe that is not an https URL: %s", ref)
		}
	}
	return refs, nil
}
