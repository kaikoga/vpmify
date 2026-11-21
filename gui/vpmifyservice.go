package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	about "github.com/kaikoga/vpmify"
	vpmify "github.com/kaikoga/vpmify/internal/app"
	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/paths"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// Events the service emits to the front end.
const (
	// eventBusy carries whether an operation is running.
	eventBusy = "busy"
	// eventLog carries one line of the core's progress messages.
	eventLog = "log"
	// eventPending says the list of recipes waiting for the user changed.
	eventPending = "pending"
)

func init() {
	application.RegisterEvent[bool](eventBusy)
	application.RegisterEvent[string](eventLog)
	application.RegisterEvent[int](eventPending)
}

// VpmifyService exposes the Go core to the front end.
type VpmifyService struct {
	core *vpmify.App
	// emit sends an event to the front end; nil until the app exists.
	emit func(name string, data any)

	// op runs one operation on the core at a time, as the CLI does in one
	// process; the core's lock only keeps processes apart.
	op sync.Mutex

	mu      sync.Mutex
	busy    int
	pending []PendingAdd
	next    int
}

func NewVpmifyService() (*VpmifyService, error) {
	base, err := paths.DefaultBase(os.Getenv)
	if err != nil {
		return nil, err
	}
	s := &VpmifyService{}
	s.core = &vpmify.App{
		Layout:      paths.Layout{Base: base},
		Client:      fetch.NewClient("vpmify-gui/" + version),
		Log:         &lineWriter{emit: func(line string) { s.send(eventLog, line) }},
		VerifyDelay: 2 * time.Second,
	}
	return s, nil
}

// AboutResult is what the About dialog shows. The release files are
// distributed under the license with the third-party notices, which the GUI
// shows here instead of as files beside it.
type AboutResult struct {
	Version string `json:"version"`
	Source  string `json:"source"`
	License string `json:"license"`
	Notices string `json:"notices"`
}

func (s *VpmifyService) About() AboutResult {
	return AboutResult{
		Version: version,
		Source:  about.SourceURL,
		License: about.License,
		Notices: about.ThirdPartyNotices,
	}
}

func (s *VpmifyService) send(name string, data any) {
	if s.emit != nil {
		s.emit(name, data)
	}
}

// run runs fn as the only operation on the core, and tells the front end
// while one is running or waiting.
func (s *VpmifyService) run(fn func() error) error {
	s.mu.Lock()
	s.busy++
	s.mu.Unlock()
	s.send(eventBusy, true)
	defer func() {
		s.mu.Lock()
		s.busy--
		busy := s.busy > 0
		s.mu.Unlock()
		s.send(eventBusy, busy)
	}()
	s.op.Lock()
	defer s.op.Unlock()
	return fn()
}

// List returns what `vpmify package list` shows.
func (s *VpmifyService) List() (vpmify.ListResult, error) {
	r, err := s.core.List()
	if err != nil {
		return vpmify.ListResult{}, err
	}
	return *r, nil
}

// Recipes lists the user's recipes.
func (s *VpmifyService) Recipes() ([]vpmify.RecipeInfo, error) {
	return s.core.Recipes()
}

// VersionsResult is what Versions shows: the version lists that could be
// read, and why the others could not.
type VersionsResult struct {
	List  *vpmify.VersionList `json:"list"`
	Error string              `json:"error,omitempty"`
}

// Versions lists the versions in the version lists of a recipe's packages,
// with prereleases when all is true. When some of the recipe's sources
// have no usable list, it lists the others with the error, as the core
// does; a failed promise would drop them.
func (s *VpmifyService) Versions(id string, all bool) (VersionsResult, error) {
	list, err := s.core.Versions(id, vpmify.VersionsOptions{All: all})
	if list == nil {
		return VersionsResult{}, err
	}
	res := VersionsResult{List: list}
	if err != nil {
		res.Error = err.Error()
	}
	return res, nil
}

// Update reads the recipes added from URLs again, and the version lists of
// the recipes.
func (s *VpmifyService) Update(ctx context.Context, ids []string) error {
	return s.run(func() error {
		_, err := s.core.Update(ctx, ids)
		return err
	})
}

// Pull makes versions available as `vpmify package pull` does: targets are "<id>"
// or "<id>/<package>", either with "@<version>".
func (s *VpmifyService) Pull(ctx context.Context, targets []string) error {
	return s.run(func() error {
		_, err := s.core.Pull(ctx, targets, vpmify.PullOptions{})
		return err
	})
}

// RemoveVersions unregisters and deletes available versions as `vpmify
// package remove` does: ref is "<id>" or "<id>/<package>", and version is
// one version, or "" for every version. The front end asks the user first.
func (s *VpmifyService) RemoveVersions(ref, version string) error {
	return s.run(func() error {
		_, err := s.core.RemoveVersions(ref, version)
		return err
	})
}

// RecipeRemoval returns what removing recipe id would remove, for the user
// to confirm.
func (s *VpmifyService) RecipeRemoval(id string) (vpmify.RemoveResult, error) {
	var res *vpmify.RemoveResult
	err := s.run(func() error {
		var err error
		res, err = s.core.RemoveRecipe(id, false)
		return err
	})
	if res == nil {
		return vpmify.RemoveResult{}, err
	}
	return *res, err
}

// RemoveRecipe removes recipe id with its available versions, as `vpmify
// recipe remove` does, after the user confirmed what RecipeRemoval showed.
func (s *VpmifyService) RemoveRecipe(id string) error {
	return s.run(func() error {
		_, err := s.core.RemoveRecipe(id, true)
		return err
	})
}

// PendingAdd is a set of recipes read from one link or one choice of files,
// waiting for the user to add them or not.
type PendingAdd struct {
	Token string `json:"token"`
	// From is the link, or the files, the recipes came from.
	From    []string       `json:"from"`
	Recipes []vpmify.Draft `json:"recipes"`
	// Error says why the recipes could not be read; there is nothing to add
	// then, only to dismiss.
	Error string `json:"error,omitempty"`
}

// Pending returns the recipes waiting for the user, oldest first. It is
// never nil, which the front end would get as null: slices.Clone keeps a nil
// slice nil, and s.pending is nil until something waits.
func (s *VpmifyService) Pending() []PendingAdd {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]PendingAdd{}, s.pending...)
}

// ConfirmAdd adds the recipes of a pending set, as they were read when they
// were shown.
func (s *VpmifyService) ConfirmAdd(ctx context.Context, token string) error {
	p, ok := s.takePending(token)
	if !ok {
		return errors.New("the recipes are no longer waiting")
	}
	if p.Error != "" {
		return errors.New(p.Error)
	}
	return s.run(func() error {
		_, err := s.core.AddDrafts(ctx, p.Recipes)
		return err
	})
}

// DiscardAdd drops a pending set without adding it.
func (s *VpmifyService) DiscardAdd(token string) {
	s.takePending(token)
}

func (s *VpmifyService) takePending(token string) (PendingAdd, bool) {
	s.mu.Lock()
	i := slices.IndexFunc(s.pending, func(p PendingAdd) bool { return p.Token == token })
	if i < 0 {
		s.mu.Unlock()
		return PendingAdd{}, false
	}
	p := s.pending[i]
	s.pending = slices.Delete(s.pending, i, i+1)
	n := len(s.pending)
	s.mu.Unlock()
	s.send(eventPending, n)
	return p, true
}

// ChooseRecipeFiles asks the user for recipe files and reads them for the
// user to confirm.
func (s *VpmifyService) ChooseRecipeFiles(ctx context.Context) error {
	files, err := application.Get().Dialog.OpenFile().
		SetTitle("Add recipes").
		CanChooseFiles(true).
		AddFilter("Recipes", "*.json").
		PromptForMultipleSelection()
	if err != nil || len(files) == 0 {
		return err
	}
	s.readForConfirmation(ctx, files, files)
	return nil
}

// receiveLink reads the recipes a vpmify: link names for the user to
// confirm. A web page can send a link at any time, so nothing is added
// until the user agrees.
func (s *VpmifyService) receiveLink(ctx context.Context, link string) {
	refs, err := recipeURLs(link)
	if err != nil {
		s.addPending(PendingAdd{From: []string{link}, Error: err.Error()})
		return
	}
	s.readForConfirmation(ctx, []string{link}, refs)
}

// receiveFiles reads recipe files dropped on the window for the user to
// confirm.
func (s *VpmifyService) receiveFiles(ctx context.Context, files []string) {
	s.readForConfirmation(ctx, files, files)
}

func (s *VpmifyService) readForConfirmation(ctx context.Context, from, refs []string) {
	var drafts []vpmify.Draft
	err := s.run(func() error {
		var err error
		drafts, err = s.core.ReadRecipes(ctx, refs...)
		return err
	})
	p := PendingAdd{From: from, Recipes: drafts}
	if err != nil {
		p.Error = err.Error()
	}
	s.addPending(p)
}

func (s *VpmifyService) addPending(p PendingAdd) {
	s.mu.Lock()
	s.next++
	p.Token = strconv.Itoa(s.next)
	if p.Recipes == nil {
		p.Recipes = []vpmify.Draft{}
	}
	s.pending = append(s.pending, p)
	n := len(s.pending)
	s.mu.Unlock()
	s.send(eventPending, n)
}

// lineWriter hands each complete line written to it to emit.
type lineWriter struct {
	mu   sync.Mutex
	buf  []byte
	emit func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}
