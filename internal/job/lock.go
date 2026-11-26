package job

import (
	"os"
	"path/filepath"
	"syscall"

	"medpipe/internal/apperr"
)

// Lock is an acquired exclusive job-key lock.
type Lock struct {
	f *os.File
}

// AcquireKeyLock serializes executions with the same normalized job key.
// Locks live under root/locks and are released by Unlock or process exit.
func AcquireKeyLock(root, jobKey string) (*Lock, error) {
	dir := filepath.Join(root, "locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, apperr.New("E_JOB_LOCK", "create lock directory: %v", err).WithStage("state")
	}
	path := filepath.Join(dir, jobKey+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, apperr.New("E_JOB_LOCK", "open lock %s: %v", path, err).WithStage("state")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, apperr.New("E_JOB_LOCK", "acquire lock %s: %v", path, err).WithStage("state")
	}
	return &Lock{f: f}, nil
}

// Unlock releases and removes the advisory lock.
func (l *Lock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	path := l.f.Name()
	unlockErr := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	closeErr := l.f.Close()
	_ = os.Remove(path)
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
