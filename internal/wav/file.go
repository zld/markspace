package wav

import "os"

// openFile isolates the OS dependency so parsing stays testable in-memory.
func openFile(path string) (*os.File, error) { return os.Open(path) }
