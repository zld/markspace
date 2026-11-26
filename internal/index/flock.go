package index

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// FileLock is an advisory cross-process mutual-exclusion lock. The keyed
// in-process mutexes serialize goroutines; this lock serializes CLI reruns
// that share the same work directory.
type FileLock struct {
	path string
	f    *os.File
}

// LockFile acquires an exclusive advisory lock, creating the file if needed.
func LockFile(path string) (*FileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &FileLock{path: path, f: f}, nil
}

// Unlock releases and closes the lock. The lock file is intentionally retained
// so waiters never race with an unlink/recreate cycle.
func (l *FileLock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	closeErr := l.f.Close()
	l.f = nil
	return errors.Join(unlockErr, closeErr)
}
