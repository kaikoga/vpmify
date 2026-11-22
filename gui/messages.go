package main

import (
	"encoding/json"
	"errors"

	vpmify "github.com/kaikoga/vpmify/internal/app"
)

// Message is a failure for the front end to show. Kind names one the front
// end says in the user's language with Args, as the CLI names the command
// that mends it; the front end shows Detail, the core's words in English,
// under it, or alone when it does not know Kind. A failure of the GUI's own
// has no Detail: the front end's words say all of it.
type Message struct {
	Kind   string            `json:"kind,omitempty"`
	Args   map[string]string `json:"args,omitempty"`
	Detail string            `json:"detail,omitempty"`
}

// errPendingGone is a pending set confirmed after it was confirmed or
// discarded, which a second click can do.
var errPendingGone = errors.New("the recipes are no longer waiting")

// messageOf is what the front end shows for err, or nil without one. Of
// several errors err joins, the first it knows names the kind; Detail says
// them all.
func messageOf(err error) *Message {
	if err == nil {
		return nil
	}
	m := &Message{Detail: err.Error()}
	var (
		noList    *vpmify.NoListError
		notListed *vpmify.NotListedError
		noMatch   *vpmify.NoMatchError
		noRecipe  *vpmify.NoRecipeError
		link      *linkError
	)
	switch {
	case errors.As(err, &link):
		return &Message{Kind: link.kind, Args: map[string]string{link.argName: link.arg}}
	case errors.Is(err, errPendingGone):
		return &Message{Kind: "pendingGone"}
	case errors.As(err, &noList):
		m.Kind, m.Args = "noList", map[string]string{"recipe": noList.Recipe}
	case errors.As(err, &notListed):
		m.Kind, m.Args = "notListed", map[string]string{"version": notListed.Version}
	case errors.As(err, &noMatch):
		m.Kind, m.Args = "noMatch", map[string]string{"package": noMatch.Package, "range": noMatch.Range}
	case errors.As(err, &noRecipe):
		m.Kind, m.Args = "noRecipe", map[string]string{"recipe": noRecipe.Recipe}
	}
	return m
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
	if m == nil || m.Kind == "" {
		return err
	}
	return &uiError{err: err, msg: m}
}
