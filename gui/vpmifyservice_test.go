package main

import (
	"context"
	"errors"
	"slices"
	"strings"
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

func TestCancelStopsTheRunningAndTheWaitingOperations(t *testing.T) {
	var logs []string
	s := &VpmifyService{emit: func(name string, data any) {
		if name == eventLog {
			logs = append(logs, data.(string))
		}
	}}
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
	if !slices.Equal(logs, []string{"canceled"}) {
		t.Errorf("logs = %v", logs)
	}

	// Operations after the cancel run.
	if err := s.run(ctx, func(ctx context.Context) error { return ctx.Err() }); err != nil {
		t.Errorf("after cancel: err = %v", err)
	}
	// With nothing running, there is nothing to say.
	s.Cancel()
	if len(logs) != 1 {
		t.Errorf("logs = %v after canceling nothing", logs)
	}
}

// A removal does not watch its context and finishes after a cancel, which
// must not be reported as canceled.
func TestCancelLetsAnOperationThatFinishesSucceed(t *testing.T) {
	s := &VpmifyService{}
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
}

func TestCanceledReadMakesNoCard(t *testing.T) {
	s := &VpmifyService{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.readForConfirmation(ctx, []string{"kit.json"}, []string{"kit.json"})
	if got := s.Pending(); len(got) != 0 {
		t.Errorf("cards = %v", got)
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
	// A link that names no recipe becomes a card without reading anything.
	for range maxLinkCards {
		s.receiveLink(ctx, "vpmify://add")
	}
	if got := len(s.Pending()); got != maxLinkCards {
		t.Fatalf("cards = %d, want %d", got, maxLinkCards)
	}
	if len(logs) != 0 {
		t.Errorf("logs = %v before the limit", logs)
	}

	s.receiveLink(ctx, "vpmify://add?url=https://example.test/kit.json")
	if got := len(s.Pending()); got != maxLinkCards {
		t.Errorf("cards = %d after the limit, want %d", got, maxLinkCards)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "ignored a link") {
		t.Errorf("logs = %v", logs)
	}

	s.DiscardAdd(s.Pending()[0].Token)
	s.receiveLink(ctx, "vpmify://add")
	if got := len(s.Pending()); got != maxLinkCards {
		t.Errorf("cards = %d after one was discarded and a link came, want %d", got, maxLinkCards)
	}
}
