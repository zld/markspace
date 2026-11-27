package mapasset

import "os"

// openFile is named separately so tests can exercise ProbeFile without a
// platform-specific build tag.
func openFile(path string) (*os.File, error) { return os.Open(path) }
