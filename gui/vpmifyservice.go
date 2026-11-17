package main

import (
	"os"
	"sync"
	"time"

	vpmify "github.com/kaikoga/vpmify/internal/app"
	"github.com/kaikoga/vpmify/internal/fetch"
	"github.com/kaikoga/vpmify/internal/paths"
)

// VpmifyService exposes the Go core to the front end.
type VpmifyService struct {
	core *vpmify.App

	mu   sync.Mutex
	urls []string
}

func NewVpmifyService() (*VpmifyService, error) {
	base, err := paths.DefaultBase(os.Getenv)
	if err != nil {
		return nil, err
	}
	return &VpmifyService{
		core: &vpmify.App{
			Layout:      paths.Layout{Base: base},
			Client:      fetch.NewClient("vpmify-gui/dev"),
			VerifyDelay: 2 * time.Second,
		},
	}, nil
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

// ReceivedURLs returns the vpmify: URLs received so far, including those that
// arrived before the front end started listening.
func (s *VpmifyService) ReceivedURLs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.urls...)
}

func (s *VpmifyService) addURL(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.urls = append(s.urls, url)
}
