package main

import (
	"encoding/json"

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

// messageOf is what the front end shows for err, or nil without one. Of
// several errors err joins, the first it knows, depth first, names the
// kind, and Detail says them all. When err also joins one it does not know,
// such as a failed download, there is no kind: the words for one failure
// would hide the other.
func messageOf(err error) *Message {
	if err == nil {
		return nil
	}
	var first *Message
	own, unknown := false, false
	walkErrors(err, func(e error) bool {
		if m, gui := kindOf(e); m != nil {
			if first == nil {
				first, own = m, gui
			}
			return false
		}
		if !wraps(e) {
			unknown = true
		}
		return true
	})
	switch {
	case first == nil || unknown:
		return &Message{Detail: err.Error()}
	case own:
		// The GUI's own failures are said whole by the front end.
		return first
	}
	first.Detail = err.Error()
	return first
}

// kindOf is the Message of one error the front end knows, without Detail,
// or nil, and whether the failure is the GUI's own rather than the core's.
func kindOf(err error) (m *Message, gui bool) {
	switch e := err.(type) {
	case *linkError:
		return &Message{Kind: e.kind, Args: map[string]string{e.argName: e.arg}}, true
	case *vpmify.NoListError:
		return &Message{Kind: "noList", Args: map[string]string{"recipe": e.Recipe}}, false
	case *vpmify.NotListedError:
		return &Message{Kind: "notListed", Args: map[string]string{"recipe": e.Recipe, "version": e.Version}}, false
	case *vpmify.NoMatchError:
		return &Message{Kind: "noMatch", Args: map[string]string{"recipe": e.Recipe, "package": e.Package, "range": e.Range, "dependent": e.Dependent}}, false
	case *vpmify.NoRecipeError:
		return &Message{Kind: "noRecipe", Args: map[string]string{"recipe": e.Recipe}}, false
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
	if m == nil || m.Kind == "" {
		return err
	}
	return &uiError{err: err, msg: m}
}
