package app

import "fmt"

// The errors below are the failures a front end can tell the user how to
// mend, each in its own words: the CLI names a command, the GUI a button.
// Their messages say what is wrong and leave the remedy to the front end.

// NoListError says that a source of recipe Recipe has no version list that
// can be used. Updating the recipe mends it.
type NoListError struct {
	Recipe string
	// Source describes the source without a list; it is empty when the
	// recipe has no version lists at all.
	Source string
	// Reason says why the list cannot be used.
	Reason string
}

func (e *NoListError) Error() string {
	if e.Source != "" {
		return fmt.Sprintf("no usable version list for %s: %s", e.Source, e.Reason)
	}
	return "no usable version list: " + e.Reason
}

// NotListedError says that the version list of recipe Recipe has no version
// Version that the user named. It may be outside the packages' versions,
// or newer than the list, which updating the recipe mends.
type NotListedError struct {
	Recipe  string
	Version string
	// Package is the package named with the version; it is empty when the
	// version was named for every package of the recipe.
	Package string
}

func (e *NotListedError) Error() string {
	if e.Package == "" {
		return fmt.Sprintf("no version list has %s: it is outside the packages' versions, or the version lists are older than it", e.Version)
	}
	return fmt.Sprintf("the version list has no %s: it is outside the package's versions, or the version list is older than it", e.Version)
}

// NoMatchError says that the version list of package Package of recipe
// Recipe has no version in Range, which Dependent, "<name> <version>" of a
// package that depends on it, asks for. The list may be older than the
// version, which updating the recipe mends.
type NoMatchError struct {
	Recipe    string
	Package   string
	Range     string
	Dependent string
}

func (e *NoMatchError) Error() string {
	return fmt.Sprintf("the version list of %s has no version that satisfies it", e.Package)
}

// NoRecipeError says that there is no recipe Recipe, which adding it mends.
type NoRecipeError struct {
	Recipe string
}

func (e *NoRecipeError) Error() string {
	return fmt.Sprintf("no recipe %q", e.Recipe)
}
