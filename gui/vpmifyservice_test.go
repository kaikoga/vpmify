package main

import (
	"context"
	"strings"
	"testing"
)

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
