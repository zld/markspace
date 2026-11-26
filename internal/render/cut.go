// Package render composites mapped intervals into deterministic MJPEG/PCM tracks.
package render

import (
	"replayctl/internal/fault"
	"replayctl/internal/timebase"
	tlpkg "replayctl/internal/timeline"
)

// Render composites the half-open millisecond window.
func (r *Renderer) Render(tl tlpkg.Timeline, startMS, endMS int64) (*Artifact, error) {
	if _, err := tl.MapWindow(startMS, endMS); err != nil {
		return nil, err
	}
	startV, err := timebase.Ms(startMS).Floor(timebase.VideoRate)
	if err != nil {
		return nil, fault.Wrap(err, fault.Render, fault.StageRender, "", -1, -1)
	}
	endV, err := timebase.Ms(endMS).Floor(timebase.VideoRate)
	if err != nil {
		return nil, fault.Wrap(err, fault.Render, fault.StageRender, "", -1, -1)
	}
	startA, err := timebase.Ms(startMS).Floor(timebase.AudioRate)
	if err != nil {
		return nil, fault.Wrap(err, fault.Render, fault.StageRender, "", -1, -1)
	}
	endA, err := timebase.Ms(endMS).Floor(timebase.AudioRate)
	if err != nil {
		return nil, fault.Wrap(err, fault.Render, fault.StageRender, "", -1, -1)
	}

	video, frames, pixels, firstFrame, err := r.renderVideo(tl, startV, endV)
	if err != nil {
		return nil, err
	}
	audio, samples, peak, err := r.renderAudio(tl, startA, endA)
	if err != nil {
		return nil, err
	}
	return &Artifact{
		Video:                     video,
		Audio:                     audio,
		Frames:                    frames,
		Pixels:                    pixels,
		AudioSamples:              samples,
		Peak:                      peak,
		VideoAlignErrorMS:         timebase.AlignError(startV, firstFrame, timebase.VideoRate),
		AudioBoundaryErrorSamples: 0,
	}, nil
}
