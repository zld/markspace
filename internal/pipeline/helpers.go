package pipeline

import (
	"errors"
	"fmt"

	"archivist/internal/model"
	"archivist/internal/timeline"
)

func (r *Runner) driftTrack(alignment *timeline.AlignmentResult) string {
	if len(alignment.Drifts) == 0 {
		return "video-unknown"
	}
	id := alignment.Drifts[0].TrackID
	if id == 2 {
		return "video-high"
	}
	return fmt.Sprintf("video-%d", id)
}

func (r *Runner) trackLabel(err error) string {
	var ae *model.ArchiveError
	if errors.As(err, &ae) {
		return fmt.Sprintf("track-%d", ae.TrackID)
	}
	return "unknown"
}
