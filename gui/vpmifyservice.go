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
		Client:      fetch.NewClient("vpmify-gui/dev"),
		Log:         &lineWriter{emit: func(line string) { s.send(eventLog, line) }},
		VerifyDelay: 2 * time.Second,
	}
	return s, nil
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

// ListResult is what `vpmify list` shows.
type ListResult struct {
	Entries []vpmify.Entry `json:"entries"`
	// SettingsError says why ALCOM's settings.json could not be read; every
	// entry then shows as unregistered.
	SettingsError string `json:"settingsError,omitempty"`
}

func (s *VpmifyService) List() (ListResult, error) {
	entries, settingsErr, err := s.core.List()
	if err != nil {
		return ListResult{}, err
	}
	r := ListResult{Entries: entries}
	if settingsErr != nil {
		r.SettingsError = settingsErr.Error()
	}
	return r, nil
}

// Recipes lists the user's recipes.
func (s *VpmifyService) Recipes() ([]vpmify.RecipeInfo, error) {
	return s.core.Recipes()
}

// Versions lists the versions in a recipe's version list, with prereleases
// when all is true.
func (s *VpmifyService) Versions(id string, all bool) (*vpmify.VersionList, error) {
	return s.core.Versions(id, vpmify.VersionsOptions{All: all})
}

// Update reads the version lists of the recipes again.
func (s *VpmifyService) Update(ctx context.Context, ids []string) error {
	return s.run(func() error { return s.core.Update(ctx, ids) })
}

// Pull makes versions available as `vpmify pull` does: targets are recipe
// ids, or "<id>@<version>".
func (s *VpmifyService) Pull(ctx context.Context, targets []string) error {
	return s.run(func() error { return s.core.Pull(ctx, targets, vpmify.PullOptions{}) })
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

// Pending returns the recipes waiting for the user, oldest first.
func (s *VpmifyService) Pending() []PendingAdd {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.pending)
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
	return s.run(func() error { return s.core.AddDrafts(ctx, p.Recipes) })
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
