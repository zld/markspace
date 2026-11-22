package session

import (
	"path/filepath"

	"avcheck/internal/ingest"
	"avcheck/internal/media"
)

// KeyRateFromIndex reads each track's MP4 sample table under dir and measures
// the fraction of sample times a keyframe index can locate. It is independent
// of the producing run: everything it reads is the export itself.
func KeyRateFromIndex(dir string, timescale int64) (hits, lookups uint64, err error) {
	clock, err := media.NewClock(timescale)
	if err != nil {
		return 0, 0, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "mp4", "track_*.mp4"))
	if err != nil {
		return 0, 0, err
	}
	for _, p := range paths {
		st, err := ingest.ReadSampleTable(p)
		if err != nil {
			return hits, lookups, err
		}
		times, err := st.Times(clock)
		if err != nil {
			return hits, lookups, err
		}
		ki, err := st.KeyIndex(clock)
		if err != nil {
			return hits, lookups, err
		}
		for _, t := range times {
			lookups++
			if _, ok := ki.Locate(t); ok {
				hits++
			}
		}
	}
	return hits, lookups, nil
}

// HitRatio returns hits/lookups, or 1 when nothing was looked up.
func HitRatio(hits, lookups uint64) float64 {
	if lookups == 0 {
		return 1
	}
	return float64(hits) / float64(lookups)
}
