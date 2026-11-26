package release

import (
	"fmt"
	"os"
	"path/filepath"
)

// ExposeLinks creates stable root-level desktop/mobile/audio paths. Each link
// is replaced atomically so callers never observe a half-switched tree.
func ExposeLinks(root, version string) error {
	links := []struct{ name, target string }{
		{"desktop", filepath.Join(VersionsDir, version, "desktop")},
		{"mobile", filepath.Join(VersionsDir, version, "mobile")},
		{"audio", filepath.Join(VersionsDir, version, "audio")},
		{"index.json", filepath.Join(VersionsDir, version, "index.json")},
	}
	for _, l := range links {
		if _, err := os.Stat(filepath.Join(root, l.target)); err != nil {
			continue // optional target, e.g. audio in a video-only package
		}
		tmp := filepath.Join(root, fmt.Sprintf(".link-%s-%d", l.name, os.Getpid()))
		_ = os.Remove(tmp)
		if err := os.Symlink(l.target, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(root, l.name)); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	return nil
}
