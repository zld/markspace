package pipeline

import "os"

// readFileOS is a thin wrapper so build.go stays free of a direct os import in
// the data path; it also gives tests a single seam to stub later if needed.
func readFileOS(path string) ([]byte, error) { return os.ReadFile(path) }
