package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/kaikoga/vpmify/internal/app"
)

// hinted is err's message with the commands that mend it, which the core's
// errors leave to the front end.
func hinted(err error) string {
	msg := err.Error()
	if hs := hints(err); len(hs) > 0 {
		msg += "; " + strings.Join(hs, "; ")
	}
	return msg
}

// hints are the commands that mend the errors err joins or wraps, each
// once, in the order the errors come.
func hints(err error) []string {
	var out []string
	walkErrors(err, func(e error) {
		if h := hint(e); h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	})
	return out
}

func hint(err error) string {
	switch e := err.(type) {
	case *app.NoListError:
		return fmt.Sprintf("run `vpmify recipe update %s`", e.Recipe)
	case *app.NotListedError:
		return fmt.Sprintf("`vpmify recipe update %s` reads it again", e.Recipe)
	case *app.NoMatchError:
		return fmt.Sprintf("`vpmify recipe update %s` reads it again", e.Recipe)
	case *app.NoRecipeError:
		return "add it with `vpmify recipe add`"
	}
	return ""
}

// walkErrors calls fn with err and every error it wraps or joins, depth
// first.
func walkErrors(err error, fn func(error)) {
	if err == nil {
		return
	}
	fn(err)
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		for _, inner := range e.Unwrap() {
			walkErrors(inner, fn)
		}
	case interface{ Unwrap() error }:
		walkErrors(e.Unwrap(), fn)
	}
}

// hintedError is an error that says the commands that mend it.
type hintedError struct{ err error }

func (e hintedError) Error() string { return hinted(e.err) }
func (e hintedError) Unwrap() error { return e.err }

// withHints is err with the commands that mend it, or nil.
func withHints(err error) error {
	if err == nil || len(hints(err)) == 0 {
		return err
	}
	return hintedError{err}
}

// hintFailures adds the commands that mend each failure to its words.
func hintFailures(fs []app.Failure) {
	for i, f := range fs {
		if f.Err() != nil {
			fs[i].Error = hinted(f.Err())
		}
	}
}

// hintDoctor adds the commands that mend each problem doctor found to its
// words.
func hintDoctor(ps []app.Problem) {
	for i, p := range ps {
		if hs := hints(p.Err()); len(hs) > 0 {
			ps[i].Message += "; " + strings.Join(hs, "; ")
		}
	}
}

// hintProblems adds the commands that mend each recipe's problem to its
// words.
func hintProblems(rs []app.RecipeInfo) {
	for i, r := range rs {
		if r.Err() != nil {
			rs[i].Problem = hinted(r.Err())
		}
	}
}
