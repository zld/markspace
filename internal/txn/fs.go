package txn

import (
	"os"
	"path/filepath"
	"syscall"
)

// pathDir returns the containing directory of a path.
func pathDir(p string) string { return filepath.Dir(p) }

// isCrossDevice reports whether a rename failed across filesystems.
func isCrossDevice(err error) bool {
	link, ok := err.(*os.LinkError)
	if !ok {
		return false
	}
	// EXDEV is the cross-device link error on Darwin/Linux.
	return link.Err == syscall.EXDEV
}
