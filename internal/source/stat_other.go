//go:build !darwin

package source

import "os"

// statIno is a no-op fallback on non-darwin platforms; size+mtime still guard
// against the common replacement case. The supported target is macOS.
func statIno(_ os.FileInfo) uint64 { return 0 }
