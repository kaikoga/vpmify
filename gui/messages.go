package main

import (
	"encoding/json"
	"fmt"

	vpmify "github.com/kaikoga/vpmify/internal/app"
)

// Message is a failure for the front end to show. Items are the failures in
// it the front end says in the user's language, a line each, as the CLI
// names the command that mends them; the front end shows Detail, the core's
// words in English, under them, or alone without Items. A failure of the
// GUI's own has no Detail: the front end's words say all of it.
type Message struct {
	Items  []Item `json:"items,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Item is one failure the front end says: Kind names its words, which Args
// fill.
type Item struct {
	Kind string            `json:"kind"`
	Args map[string]string `json:"args,omitempty"`
}

// messageOf is what the front end shows for err, or nil without one. Each
// error err wraps or joins that the front end knows is an Item, depth
// first, once however often it is joined (the sources of one recipe fail
// alike), and Detail says them all. Every one is said: the first alone
// would name one recipe of several, and updating it does not help when
// another is the one that lacks the version. When err also joins one it
// does not know, such as a failed download, there are no Items: the words
// for one failure would hide the other.
func messageOf(err error) *Message {
	if err == nil {
		return nil
	}
	var items []Item
	seen := map[string]bool{}
	own, unknown := true, false
	walkErrors(err, func(e error) bool {
		if it, gui := kindOf(e); it != nil {
			// fmt prints a map's keys sorted.
			key := fmt.Sprint(it.Kind, it.Args)
			if !seen[key] {
				seen[key] = true
				items = append(items, *it)
			}
			own = own && gui
			return false
		}
		if !wraps(e) {
			unknown = true
		}
		return true
	})
	switch {
	case len(items) == 0 || unknown:
		return &Message{Detail: err.Error()}
	case own:
		// The GUI's own failures are said whole by the front end.
		return &Message{Items: items}
	}
	return &Message{Items: items, Detail: err.Error()}
}

// kindOf is the Item of one error the front end knows, or nil, and whether
// the failure is the GUI's own rather than the core's.
func kindOf(err error) (it *Item, gui bool) {
	switch e := err.(type) {
	case *linkError:
		return &Item{Kind: e.kind, Args: map[string]string{e.argName: e.arg}}, true
	case *vpmify.NoListError:
		return &Item{Kind: "noList", Args: map[string]string{"recipe": e.Recipe}}, false
	case *vpmify.NotListedError:
		return &Item{Kind: "notListed", Args: map[string]string{"recipe": e.Recipe, "version": e.Version}}, false
	case *vpmify.NoMatchError:
		return &Item{Kind: "noMatch", Args: map[string]string{"recipe": e.Recipe, "package": e.Package, "range": e.Range, "dependent": e.Dependent}}, false
	case *vpmify.NoRecipeError:
		return &Item{Kind: "noRecipe", Args: map[string]string{"recipe": e.Recipe}}, false
	}
	return nil, false
}

// wraps says whether err only wraps or joins other errors, which say the
// failure.
func wraps(err error) bool {
	switch err.(type) {
	case interface{ Unwrap() []error }, interface{ Unwrap() error }:
		return true
	}
	return false
}

// walkErrors calls fn with err and, while fn returns true, the errors it
// wraps or joins, depth first.
func walkErrors(err error, fn func(error) bool) {
	if err == nil || !fn(err) {
		return
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		for _, inner := range e.Unwrap() {
			walkErrors(inner, fn)
		}
	case interface{ Unwrap() error }:
		walkErrors(e.Unwrap(), fn)
	}
}

// uiError is an error a method returns to the front end with its Message,
// which the front end gets as the cause of the failed call.
type uiError struct {
	err error
	msg *Message
}

func (e *uiError) Error() string                { return e.err.Error() }
func (e *uiError) Unwrap() error                { return e.err }
func (e *uiError) MarshalJSON() ([]byte, error) { return json.Marshal(e.msg) }

// describe is err for a method to return to the front end: with its
// Message when the front end can say it in the user's language, and as it
// is otherwise.
func describe(err error) error {
	m := messageOf(err)
	if m == nil || len(m.Items) == 0 {
		return err
	}
	return &uiError{err: err, msg: m}
}
