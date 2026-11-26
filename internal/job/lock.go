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

// Unlock releases the advisory lock.  The lock file is intentionally retained:
// unlinking it can race with another process opening the same path and allow a
// second inode to be locked.
func (l *Lock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	if err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN); err != nil {
		_ = l.f.Close()
		return err
	}
	return l.f.Close()
}
