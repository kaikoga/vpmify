// Package lock takes the machine-wide lock that keeps two Vpmify processes
// from changing the store and ALCOM's settings at the same time. The OS
// releases the lock when the process ends, even when it crashes.
package lock

import (
	"os"
	"path/filepath"
)

// Lock is a held lock.
type Lock struct {
	f *os.File
}

// Acquire takes the lock at path, waiting while another process holds it.
// onWait is called once before waiting.
func Acquire(path string, onWait func()) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := acquire(path, onWait)
	if err != nil {
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release gives the lock back.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := release(l.f)
	l.f = nil
	return err
}
