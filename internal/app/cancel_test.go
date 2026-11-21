package app

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// A cached archive is converted without reaching the network, so only
// Pull's own checks stop it.
func TestCanceledPullDoesNotConvertFromTheCache(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if err := f.addAndPull(PullOptions{}, f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	if _, err := f.app.RemoveVersions("kit", "1.0.0"); err != nil {
		t.Fatal(err, f.log)
	}

	before := f.log.Len()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := f.app.Pull(ctx, nil, PullOptions{})
	if err == nil {
		t.Error("no error from a canceled pull")
	}
	if res == nil || len(res.Built) != 0 {
		t.Errorf("result = %+v, want nothing built", res)
	}
	if got := f.available(); len(got) != 0 {
		t.Errorf("placed = %v", got)
	}
	if log := f.log.String()[before:]; strings.Contains(log, "converting") {
		t.Errorf("converted after cancel:\n%s", log)
	}
}

func TestCanceledPullRegistersWhatItBuilt(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	f.reg.add("1.1.0", "2021.3")
	if _, err := f.app.Add(context.Background(), f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}

	// The user cancels while the first archive downloads; the fake
	// registry still serves it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.reg.handler = func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, ".tgz") {
			cancel()
		}
		f.reg.serve(w, req)
	}
	res, err := f.app.Pull(ctx, nil, PullOptions{All: true})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("err = %v", err)
	}
	if res == nil || len(res.Built) != 1 {
		t.Fatalf("result = %+v, want one version built", res)
	}
	// The version stopped at says why, once.
	if len(res.Errors) != 1 {
		t.Errorf("errors = %+v", res.Errors)
	}
	if n := strings.Count(f.log.String(), "converting"); n != 1 {
		t.Errorf("converting %d versions:\n%s", n, f.log)
	}
	want := f.app.Layout.PackageDir("kit", pkgName, res.Built[0].Version)
	if got := f.registered(); !slices.Contains(got, want) {
		t.Errorf("registered = %v, want %s among them", got, want)
	}
	if got := f.available(); len(got) != 1 {
		t.Errorf("placed = %v", got)
	}
	if strings.Contains(f.log.String(), "up to date") {
		t.Errorf("a canceled pull said it was up to date:\n%s", f.log)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// The CLI's Ctrl-C cancels with a cause of its own, while net/http's HTTP/2
// transport fails with the context's plain error.
func TestPullCanceledWithACauseSaysWhyOnce(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if _, err := f.app.Add(context.Background(), f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	f.app.Client.HTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cancel(errors.New("interrupt signal received"))
		return nil, req.Context().Err()
	})
	res, err := f.app.Pull(ctx, nil, PullOptions{})
	if err == nil {
		t.Error("no error from a canceled pull")
	}
	if res == nil || len(res.Errors) != 1 {
		t.Errorf("result = %+v, want one error", res)
	}
}

// The GUI may cancel adding while the core waits for another process's
// lock.
func TestCanceledAddSavesNothing(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	drafts, err := f.app.ReadRecipes(context.Background(), f.writeRecipe(""))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := f.app.AddDrafts(ctx, drafts)
	if err == nil || res != nil {
		t.Errorf("res = %+v, err = %v", res, err)
	}
	if ids, err := f.app.recipeIDs(); err != nil || len(ids) != 0 {
		t.Errorf("recipes = %v, err = %v", ids, err)
	}
}

func TestCanceledUpdateReadsNothing(t *testing.T) {
	f := newFixture(t)
	f.reg.add("1.0.0", "2021.3")
	if _, err := f.app.Add(context.Background(), f.writeRecipe("")); err != nil {
		t.Fatal(err, f.log)
	}
	var requests []string
	f.reg.handler = func(w http.ResponseWriter, req *http.Request) {
		requests = append(requests, req.URL.Path)
		f.reg.serve(w, req)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := f.app.Update(ctx, nil)
	if err == nil {
		t.Error("no error from a canceled update")
	}
	if len(requests) != 0 {
		t.Errorf("requests = %v", requests)
	}
	if res == nil || !reflect.DeepEqual(res.Errors, []Failure{{Error: "context canceled"}}) {
		t.Errorf("result = %+v", res)
	}
}
