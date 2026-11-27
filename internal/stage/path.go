package stage

import "path/filepath"

// joinPath isolates path construction for test portability.
func joinPath(dir, rel string) string { return filepath.Join(dir, rel) }
