package engine

import "mediad/timeline"

// generateTracks is replaced by the real multi-track fixture generator in the
// following commit; the stub keeps the package buildable at each step.
func generateTracks(cfg Config, framesN, frameBytes int) ([]timeline.Track, error) {
	return nil, nil
}
