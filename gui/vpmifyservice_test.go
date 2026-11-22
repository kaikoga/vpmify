package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitBusy waits until n operations are running or waiting.
func waitBusy(t *testing.T, s *VpmifyService, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		busy := s.busy
		s.mu.Unlock()
		if busy == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("busy = %d, want %d", busy, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// logRecorder keeps the log lines a service sends, from any goroutine.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) emit(name string, data any) {
	if name == eventLog {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.lines = append(r.lines, data.(string))
	}
}

func (r *logRecorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.lines)
}

func TestCancelStopsTheRunningAndTheWaitingOperations(t *testing.T) {
	var logs logRecorder
	s := &VpmifyService{emit: logs.emit}
	ctx := context.Background()
	started := make(chan struct{})
	running := make(chan error)
	go func() {
		running <- s.run(ctx, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-started
	ran := false
	waiting := make(chan error)
	go func() {
		waiting <- s.run(ctx, func(context.Context) error {
			ran = true
			return nil
		})
	}()
	waitBusy(t, s, 2)

	s.Cancel()
	if err := <-running; !errors.Is(err, errCanceled) {
		t.Errorf("running: err = %v", err)
	}
	if err := <-waiting; !errors.Is(err, errCanceled) {
		t.Errorf("waiting: err = %v", err)
	}
	if ran {
		t.Error("the waiting operation ran after it was canceled")
	}
	// Both stopped, and the log says so once.
	if got, want := logs.get(), []string{"canceling", "canceled"}; !slices.Equal(got, want) {
		t.Errorf("logs = %v, want %v", got, want)
	}

	// Operations after the cancel run.
	if err := s.run(ctx, func(ctx context.Context) error { return ctx.Err() }); err != nil {
		t.Errorf("after cancel: err = %v", err)
	}
	// With nothing running, there is nothing to say.
	s.Cancel()
	if got := logs.get(); len(got) != 2 {
		t.Errorf("logs = %v after canceling nothing", got)
	}
}

// A removal does not watch its context and finishes after a cancel, which
// must not be reported as canceled.
func TestCancelLetsAnOperationThatFinishesSucceed(t *testing.T) {
	var logs logRecorder
	s := &VpmifyService{emit: logs.emit}
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error)
	go func() {
		done <- s.run(context.Background(), func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	s.Cancel()
	close(release)
	if err := <-done; err != nil {
		t.Errorf("err = %v", err)
	}
	if got, want := logs.get(), []string{"canceling"}; !slices.Equal(got, want) {
		t.Errorf("logs = %v, want %v", got, want)
	}
}

// A cancel of the caller's context alone, such as the front end's, is not
// the user's cancel.
func TestCancelOfTheCallerIsNotLogged(t *testing.T) {
	var logs logRecorder
	s := &VpmifyService{emit: logs.emit}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.run(ctx, func(context.Context) error { return nil }); !errors.Is(err, errCanceled) {
		t.Errorf("err = %v", err)
	}
	if got := logs.get(); len(got) != 0 {
		t.Errorf("logs = %v", got)
	}
}

// A failure that came before the cancel, or that an operation not watching
// its context meets after it, is still reported.
func TestCancelKeepsFailuresOfTheirOwn(t *testing.T) {
	real := errors.New("upstream is broken")
	for name, fn := range map[string]func(ctx context.Context) error{
		"joined with the cancel": func(ctx context.Context) error {
			<-ctx.Done()
			return errors.Join(fmt.Errorf("a: %w", real), fmt.Errorf("b: %w", context.Cause(ctx)))
		},
		"wrapping a join": func(ctx context.Context) error {
			<-ctx.Done()
			return fmt.Errorf("kit: %w", errors.Join(real, ctx.Err()))
		},
		"not watching the context": func(ctx context.Context) error {
			<-ctx.Done()
			return real
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := &VpmifyService{}
			started := make(chan struct{})
			done := make(chan error)
			go func() {
				done <- s.run(context.Background(), func(ctx context.Context) error {
					close(started)
					return fn(ctx)
				})
			}()
			<-started
			s.Cancel()
			if err := <-done; !errors.Is(err, real) {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestOnlyCanceled(t *testing.T) {
	canceled := fmt.Errorf("GET x: %w", context.Canceled)
	if !onlyCanceled(errors.Join(fmt.Errorf("kit: %w", canceled), context.Canceled)) {
		t.Error("cancels alone are not only canceled")
	}
	if onlyCanceled(errors.New("context canceled")) {
		t.Error("an error that only reads like the cancel is canceled")
	}
}

func TestCanceledReadMakesNoCard(t *testing.T) {
	s := &VpmifyService{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.readForConfirmation(ctx, []string{"kit.json"}, []string{"kit.json"})
	if got := s.Pending(); len(got) != 0 {
		t.Errorf("pending = %v", got)
	}
}

func TestReceiveLinkIgnoresLinksPastTheLimit(t *testing.T) {
	var logs []string
	s := &VpmifyService{emit: func(name string, data any) {
		if name == eventLog {
			logs = append(logs, data.(string))
		}
	}}
	ctx := context.Background()
	// A link that names no recipe waits for the user without reading
	// anything.
	for range maxLinkCards {
		s.receiveLink(ctx, "vpmify://recipe/add")
	}
	if got := len(s.Pending()); got != maxLinkCards {
		t.Fatalf("pending = %d, want %d", got, maxLinkCards)
	}
	if len(logs) != 0 {
		t.Errorf("logs = %v before the limit", logs)
	}

	s.receiveLink(ctx, "vpmify://recipe/add?url=https://example.test/kit.json")
	if got := len(s.Pending()); got != maxLinkCards {
		t.Errorf("pending = %d after the limit, want %d", got, maxLinkCards)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "ignored a link") {
		t.Errorf("logs = %v", logs)
	}

	s.DiscardAdd(s.Pending()[0].Token)
	s.receiveLink(ctx, "vpmify://recipe/add")
	if got := len(s.Pending()); got != maxLinkCards {
		t.Errorf("pending = %d after one was discarded and a link came, want %d", got, maxLinkCards)
	}
}
