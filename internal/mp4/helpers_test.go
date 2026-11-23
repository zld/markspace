package mp4

import "os"

// openAt opens the scanned file for payload reads in tests.
func openAt(_ TrackInfo, path string) (*os.File, error) { return os.Open(path) }
