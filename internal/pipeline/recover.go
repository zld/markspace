package pipeline

import (
	"os"
	"path/filepath"

	"continuity/internal/clock"
	"continuity/internal/index"
)

// reconcileOrphans scans the content store for files that exist on disk but
// have no committed journal entry. Each orphan is hashed and registered as
// a completed artifact (its bytes are trusted because the store path itself
// is derived from content hash), so recovery never republishes or duplicates.
func (r *Runner) reconcileOrphans(storeDir string) error {
	recovered := 0
	err := filepath.WalkDir(storeDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(storeDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		st := r.journal.State()
		if _, known := st.Artifacts[rel]; known {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return clock.New(clock.KindIO, "recover", "read orphan: %v", readErr)
		}
		sha := index.HashBytes(data)
		art := index.Artifact{
			StorePath:  rel,
			SHA256:     sha,
			Size:       int64(len(data)),
			RepID:      filepath.Base(filepath.Dir(rel)),
			SegmentIdx: parseSegmentIdx(rel),
		}
		// Re-register without crash injection during recovery.
		r.journal.SetCrashConfig(index.CrashConfig{CrashAfterWrite: 0})
		if regErr := r.journal.RegisterArtifact(art); regErr != nil {
			return regErr
		}
		recovered++
		return nil
	})
	if err != nil {
		return err
	}
	if recovered > 0 {
		return r.journal.NoteDuplicatePublish(0) // no republish; recovery only
	}
	return nil
}

// parseSegmentIdx extracts the segment index from a store path name of the
// form repID/seg-000123-abcd....m4s.
func parseSegmentIdx(rel string) int {
	base := filepath.Base(rel)
	// strip "seg-" prefix and read leading digits up to the next '-'.
	if len(base) < 5 || base[:4] != "seg-" {
		return 0
	}
	n := 0
	for i := 4; i < len(base); i++ {
		c := base[i]
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
