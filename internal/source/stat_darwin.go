//go:build darwin

package source

import (
	"os"
	"syscall"
)

// statIno returns the inode so that a same-size, same-mtime replacement is
// still detectable.
func statIno(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino
	}
	return 0
}
