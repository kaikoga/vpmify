package app

import (
	"context"
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
