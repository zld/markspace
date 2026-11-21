package fixture

import "os"

// readFileOS is a tiny indirection kept so tests do not import os directly in
// the main test file's helper region.
func readFileOS(path string) ([]byte, error) { return os.ReadFile(path) }
