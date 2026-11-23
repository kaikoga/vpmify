package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	// eventBusy carries whether an operation is running or waiting.
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

	// busyMu keeps each change of busy and its event together, so that the
	// front end gets them in order: the last event it gets is what busy is.
	busyMu sync.Mutex

	mu sync.Mutex
	// op is held by the one operation running on the core, as the CLI runs
	// one in a process; the core's lock only keeps processes apart. It is a
	// channel rather than a sync.Mutex so that waiting for it can be
	// canceled.
	op chan struct{}
	// ops is the context of the operations running and waiting, which
	// cancelOps cancels. stopped is ops's own, so that the log says once
	// for each cancel that something stopped.
	ops       context.Context
	cancelOps context.CancelFunc
	stopped   *sync.Once
	busy      int
	pending   []PendingAdd
	next      int
	// reading counts the links being read for the user to confirm, which
	// do not wait in pending yet but will.
	reading int
}

// errCanceled is returned by run when Cancel stopped the operation. The
// methods the front end calls return nothing for it instead of an error,
// since the user asked for it; run says so in the log.
var errCanceled = errors.New("canceled")

// maxLinkCards is how many sets of recipes may wait for the user before
// more links are ignored. A web page can send links at any time, and each
// set keeps the recipes it read; people who mean to add recipes do not
// reach it.
const maxLinkCards = 20

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

// AboutResult is what the About tab shows. The release files are
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

// initOps makes op and ops on first use, so that a VpmifyService made
// without NewVpmifyService works. s.mu must be held.
func (s *VpmifyService) initOps() {
	if s.op == nil {
		s.op = make(chan struct{}, 1)
	}
	if s.ops == nil {
		s.newOps()
	}
}

// newOps starts the context of the operations to come. s.mu must be held.
func (s *VpmifyService) newOps() {
	s.ops, s.cancelOps = context.WithCancel(context.Background())
	s.stopped = new(sync.Once)
}

// run runs fn as the only operation on the core, and tells the front end
// while one is running or waiting. fn gets a context that Cancel cancels
// as well as ctx. It returns errCanceled when canceled before fn started,
// or when fn failed after being canceled with nothing but the cancel to
// say; a failure of its own still comes back, as does the result of an fn
// that does not watch its context, such as a removal.
func (s *VpmifyService) run(ctx context.Context, fn func(context.Context) error) error {
	s.busyMu.Lock()
	s.mu.Lock()
	s.initOps()
	s.busy++
	op, ops, stopped := s.op, s.ops, s.stopped
	s.mu.Unlock()
	s.send(eventBusy, true)
	s.busyMu.Unlock()
	// canceled says in the log that Cancel stopped something, once for the
	// operations it stopped together. A cancel of ctx alone is not the
	// user's, and is not said.
	canceled := func() error {
		if ops.Err() != nil {
			stopped.Do(func() { s.send(eventLog, "canceled") })
		}
		return errCanceled
	}
	defer func() {
		s.busyMu.Lock()
		defer s.busyMu.Unlock()
		s.mu.Lock()
		s.busy--
		busy := s.busy > 0
		s.mu.Unlock()
		s.send(eventBusy, busy)
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(ops, cancel)()
	select {
	case op <- struct{}{}:
	case <-ctx.Done():
		return canceled()
	}
	defer func() { <-op }()
	// The cancel that stopped the operation before this one frees op, and
	// may come first: select picks at random between ready cases, and
	// AfterFunc cancels ctx in a goroutine of its own. ops is canceled by
	// the time Cancel returns.
	if ctx.Err() != nil || ops.Err() != nil {
		return canceled()
	}
	err := fn(ctx)
	if err != nil && ctx.Err() != nil && onlyCanceled(err) {
		return canceled()
	}
	return err
}

// onlyCanceled reports whether every error err joins is a cancel, so that
// a failure that happened before the cancel is not hidden with it.
func onlyCanceled(err error) bool {
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		for _, inner := range e.Unwrap() {
			if !onlyCanceled(inner) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return err == context.Canceled || onlyCanceled(e.Unwrap())
	}
	return err == context.Canceled
}

// Cancel stops the operations running and waiting to run. The core stops
// an operation at its next download or version, so the one running may take
// a moment. One waiting for another Vpmify process to release the core's
// lock is not stopped while it waits, as the core waits without a context;
// once it gets the lock, adding, updating and pulling stop, while a removal
// goes on. The log says "canceling" at once, and "canceled" when something
// stopped.
func (s *VpmifyService) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initOps()
	if s.busy > 0 {
		// Sent before the cancel, so that it comes before "canceled".
		s.send(eventLog, "canceling")
	}
	s.cancelOps()
	s.newOps()
}

// quiet turns errCanceled into no error, for the methods the front end
// calls.
func quiet(err error) error {
	if errors.Is(err, errCanceled) {
		return nil
	}
	return err
}

// List returns what `vpmify package list` shows.
func (s *VpmifyService) List() (vpmify.ListResult, error) {
	r, err := s.core.List()
	if err != nil {
		return vpmify.ListResult{}, describe(err)
	}
	return *r, nil
}

// RecipeItem is one of the user's recipes, as `vpmify recipe list` shows it,
// with its problem as a Message.
type RecipeItem struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName,omitempty"`
	Packages    []string `json:"packages"`
	Origin      string   `json:"origin,omitempty"`
	Problem     *Message `json:"problem,omitempty"`
}

// Recipes lists the user's recipes.
func (s *VpmifyService) Recipes() ([]RecipeItem, error) {
	recipes, err := s.core.Recipes()
	if err != nil {
		return nil, describe(err)
	}
	out := make([]RecipeItem, 0, len(recipes))
	for _, r := range recipes {
		item := RecipeItem{ID: r.ID, DisplayName: r.DisplayName, Packages: r.Packages, Origin: r.Origin}
		if r.Problem != "" {
			item.Problem = messageOf(r.Err())
			if item.Problem == nil {
				item.Problem = &Message{Detail: r.Problem}
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// VersionsResult is what Versions shows: the version lists that could be
// read, and why the others could not.
type VersionsResult struct {
	List  *vpmify.VersionList `json:"list"`
	Error *Message            `json:"error,omitempty"`
}

// Versions lists the versions in the version lists of a recipe's packages,
// with prereleases when all is true. When some of the recipe's sources
// have no usable list, it lists the others with the error, as the core
// does; a failed promise would drop them.
func (s *VpmifyService) Versions(id string, all bool) (VersionsResult, error) {
	list, err := s.core.Versions(id, vpmify.VersionsOptions{All: all})
	if list == nil {
		return VersionsResult{}, describe(err)
	}
	return VersionsResult{List: list, Error: messageOf(err)}, nil
}

// Update reads the recipes added from URLs again, and the version lists of
// the recipes.
func (s *VpmifyService) Update(ctx context.Context, ids []string) error {
	return describe(quiet(s.run(ctx, func(ctx context.Context) error {
		_, err := s.core.Update(ctx, ids)
		return err
	})))
}

// Sync registers with ALCOM again the versions Vpmify keeps that ALCOM does
// not list, as `vpmify sync` does.
func (s *VpmifyService) Sync(ctx context.Context) error {
	return describe(quiet(s.run(ctx, func(context.Context) error {
		_, err := s.core.Sync()
		return err
	})))
}

// Pull makes versions available as `vpmify package pull` does: targets are "<id>"
// or "<id>/<package>", either with "@<version>". prerelease is
// `--prerelease`.
func (s *VpmifyService) Pull(ctx context.Context, targets []string, prerelease bool) error {
	return describe(quiet(s.run(ctx, func(ctx context.Context) error {
		_, err := s.core.Pull(ctx, targets, vpmify.PullOptions{Prerelease: prerelease})
		return err
	})))
}

// RemoveVersions unregisters and deletes the versions in the store as
// `vpmify package remove` does: ref is "<id>" or "<id>/<package>", and
// version is one version, or "" for every version. The front end asks the
// user first.
func (s *VpmifyService) RemoveVersions(ctx context.Context, ref, version string) error {
	return describe(quiet(s.run(ctx, func(context.Context) error {
		_, err := s.core.RemoveVersions(ref, version)
		return err
	})))
}

// RecipeRemoval returns what removing recipe id would remove, for the user
// to confirm, or nil when canceled before it was read.
func (s *VpmifyService) RecipeRemoval(ctx context.Context, id string) (*vpmify.RemoveResult, error) {
	var res *vpmify.RemoveResult
	err := s.run(ctx, func(context.Context) error {
		var err error
		res, err = s.core.RemoveRecipe(id, false)
		return err
	})
	if errors.Is(err, errCanceled) {
		return nil, nil
	}
	if res == nil {
		return &vpmify.RemoveResult{}, describe(err)
	}
	return res, describe(err)
}

// RemoveRecipe removes recipe id with its versions in the store, as `vpmify
// recipe remove` does, after the user confirmed what RecipeRemoval showed.
func (s *VpmifyService) RemoveRecipe(ctx context.Context, id string) error {
	return describe(quiet(s.run(ctx, func(context.Context) error {
		_, err := s.core.RemoveRecipe(id, true)
		return err
	})))
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
	Error *Message `json:"error,omitempty"`
	err   error
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
		// Confirmed or discarded already, as a second click can do: the
		// first took care of it, and there is nothing to say.
		return nil
	}
	if p.err != nil {
		return describe(p.err)
	}
	return describe(quiet(s.run(ctx, func(ctx context.Context) error {
		_, err := s.core.AddDrafts(ctx, p.Recipes)
		return err
	})))
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
	s.mu.Lock()
	full := len(s.pending)+s.reading >= maxLinkCards
	if !full {
		s.reading++
	}
	s.mu.Unlock()
	if full {
		s.send(eventLog, fmt.Sprintf("ignored a link, as %d sets of recipes already wait for you to add or discard them: %s", maxLinkCards, link))
		return
	}
	defer func() {
		s.mu.Lock()
		s.reading--
		s.mu.Unlock()
	}()
	refs, err := recipeURLs(link)
	if err != nil {
		s.addPending(PendingAdd{From: []string{link}, Error: messageOf(err), err: err})
		return
	}
	s.readForConfirmation(ctx, []string{link}, refs)
}

// receiveFiles reads recipe files dropped on the window for the user to
// confirm.
func (s *VpmifyService) receiveFiles(ctx context.Context, files []string) {
	s.readForConfirmation(ctx, files, files)
}

// readForConfirmation reads the recipes for the user to confirm. A
// canceled read leaves nothing to confirm: the user stopped it, and asking
// would only say so.
func (s *VpmifyService) readForConfirmation(ctx context.Context, from, refs []string) {
	var drafts []vpmify.Draft
	err := s.run(ctx, func(ctx context.Context) error {
		var err error
		drafts, err = s.core.ReadRecipes(ctx, refs...)
		return err
	})
	if errors.Is(err, errCanceled) {
		return
	}
	p := PendingAdd{From: from, Recipes: drafts, Error: messageOf(err), err: err}
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
