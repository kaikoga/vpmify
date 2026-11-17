//go:build windows

package lock

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// errorSharingViolation is ERROR_SHARING_VIOLATION, which CreateFile returns
// while another process holds the file open without sharing.
const errorSharingViolation syscall.Errno = 32

// acquire opens the lock file with no sharing. The handle itself is the
// lock: Windows closes it when the process ends.
func acquire(path string, onWait func()) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	waited := false
	for {
		h, err := syscall.CreateFile(name,
			syscall.GENERIC_READ|syscall.GENERIC_WRITE,
			0, // no sharing
			nil,
			syscall.OPEN_ALWAYS,
			syscall.FILE_ATTRIBUTE_NORMAL,
			0)
		if err == nil {
			return os.NewFile(uintptr(h), path), nil
		}
		if !errors.Is(err, errorSharingViolation) {
			return nil, err
		}
		if !waited {
			waited = true
			if onWait != nil {
				onWait()
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func release(f *os.File) error {
	return f.Close()
}
