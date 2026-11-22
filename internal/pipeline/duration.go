package pipeline

import (
	"fmt"

	"mediapipe/internal/clock"
	"mediapipe/internal/mp4"
)

// CheckDurations verifies that every declared track and movie duration is
// consistent with the media actually present in the fragments. A container
// whose declaration is shorter than its media is rejected: downstream
// segmenters would otherwise truncate real samples.
func CheckDurations(f *mp4.File) error {
	if f.Timescale == 0 {
		return fmt.Errorf("pipeline: movie timescale 0: %w", clock.ErrZeroTimescale)
	}
	endByTrack := map[uint32]uint64{}
	for _, seg := range f.Segments {
		for _, st := range seg.Tracks {
			for _, s := range st.Samples {
				end := s.DTS + uint64(s.Duration)
				if end > endByTrack[st.TrackID] {
					endByTrack[st.TrackID] = end
				}
			}
		}
	}
	var movieEnd int64
	for _, t := range f.Tracks {
		end := endByTrack[t.ID]
		if t.Duration != 0 {
			if err := clock.CheckDeclaredDuration(t.Duration, t.Timescale, int64(end)); err != nil {
				return fmt.Errorf("pipeline: track %d: %w", t.ID, err)
			}
		}
		unified, err := clock.Convert(int64(end), t.Timescale, f.Timescale)
		if err != nil {
			return fmt.Errorf("pipeline: track %d duration conversion: %w", t.ID, err)
		}
		if unified > movieEnd {
			movieEnd = unified
		}
	}
	if f.Duration != 0 {
		if err := clock.CheckDeclaredDuration(f.Duration, f.Timescale, movieEnd); err != nil {
			return fmt.Errorf("pipeline: movie: %w", err)
		}
	}
	return nil
}
