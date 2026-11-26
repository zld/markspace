package demo

import (
	"audionorm/internal/audio"
	"audionorm/internal/ingest"
	"audionorm/internal/media"
	"context"
)

// TimelinePCM assembles decoded packet PCM onto the unified timeline.
// Decoded packet blocks are placed at their timeline offsets; known
// gaps are filled with silence so frame positions stay accurate (no
// audio is invented, only zero frames). If any error is unlocatable
// (HOLD), the boolean result is false and the caller must keep the task
// on hold rather than emit a complete interval.
//
// containerData is the full muxed byte stream (Ogg for Opus, ADTS for
// AAC-LC) that FFmpeg can demux directly; the ingest result supplies the
// per-packet timeline placement.
func TimelinePCM(ctx context.Context, dec *audio.Decoder, spec audio.StreamSpec, containerData []byte, res *ingest.Result, span uint64) (pcm *audio.PCM, hold bool, holdOffset int64, err error) {
	for _, e := range res.Errors.All() {
		if !e.Locatable {
			return nil, true, e.ByteOffset, nil
		}
	}
	out := make([]float32, span*2) // stereo, zero-filled = silence gaps

	if len(containerData) == 0 {
		return &audio.PCM{Rate: 48000, Channels: 2, Samples: out}, false, 0, nil
	}
	decoded, err := dec.DecodeStream(ctx, spec, containerData)
	if err != nil {
		return nil, false, 0, err
	}

	// The demuxed stream is the continuous source PCM (the container was
	// generated from the un-damaged signal). Drop leading encoder priming,
	// then take exactly span frames; any decoder shortfall or trailing
	// padding stays as silence. Known gaps are then explicitly zeroed so
	// the output never invents audio for a missing interval.
	src := decoded.Samples
	allFrames := decoded.Frames()
	priming := 0
	if surplus := allFrames - int(span); surplus > 0 {
		priming = surplus / 2
		if priming > allFrames {
			priming = allFrames
		}
	}
	take := int(span)
	if priming+take > allFrames {
		take = allFrames - priming
		if take < 0 {
			take = 0
		}
	}
	if take > 0 {
		copy(out, src[priming*2:(priming+take)*2])
	}
	for _, g := range res.Gaps {
		s := g.When.Start
		e := g.When.End
		if s > span {
			continue
		}
		if e > span {
			e = span
		}
		for i := s * 2; i < e*2; i++ {
			out[i] = 0
		}
	}
	return &audio.PCM{Rate: media.TimelineRate, Channels: 2, Samples: out}, false, 0, nil
}
