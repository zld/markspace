package demo

import (
	"context"
	"fmt"

	"mediarc/model"
	"mediarc/render"
	rtpkernel "mediarc/rtp"
)

func (r *runner) runRender(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return model.New(model.CodeCanceled, "demo", "canceled before render").WithCause(err)
	}
	renderer, err := render.New(render.Config{})
	if err != nil {
		return err
	}
	timeline := render.Timeline{Slots: slotsFromSegments(r.segments)}
	first := render.Measure(renderer.RenderFrames(timeline), renderer.RenderAudio(timeline))
	second := render.Measure(renderer.RenderFrames(timeline), renderer.RenderAudio(timeline))

	frames := renderer.RenderFrames(timeline)
	chunks := renderer.RenderAudio(timeline)
	summary := render.Summarise(frames, chunks)

	var recoveredSlots int
	for _, slot := range timeline.Slots {
		if slot.Kind != render.SlotMedia {
			recoveredSlots++
		}
	}
	checks := []Assertion{
		assert("render output is reproducible run to run", fmt.Sprintf("%016x/%016x", first.FrameHash, first.AudioHash),
			fmt.Sprintf("%016x/%016x", second.FrameHash, second.AudioHash),
			first.FrameHash == second.FrameHash && first.AudioHash == second.AudioHash),
		assert("rendered frame count matches the timeline", fmt.Sprint(first.Pixels.Frames), fmt.Sprint(len(timeline.Slots)),
			first.Pixels.Frames == len(timeline.Slots)),
		assert("pixel mean lies in range", fmt.Sprintf("%.2f", first.Pixels.MeanLuma), "(0,255)",
			first.Pixels.MeanLuma > 0 && first.Pixels.MeanLuma < 255),
		assert("peak loudness is reported", render.FormatLoudness(first.Audio.PeakDBFS), "<=0 dBFS",
			first.Audio.PeakDBFS <= 0),
		assert("recovered slots render as silence", fmt.Sprint(first.Audio.RecoveredChunks), fmt.Sprint(recoveredSlots),
			first.Audio.RecoveredChunks == recoveredSlots),
	}
	r.metrics.FrameHash = first.FrameHash
	r.metrics.SegmentHash = first.AudioHash
	r.metrics.PixelMean = first.Pixels.MeanLuma
	r.metrics.PeakDBFS = first.Audio.PeakDBFS
	r.metrics.FrameCount = len(frames)
	r.metrics.SegmentCount = len(chunks)
	_ = summary
	r.record(Scenario{
		Name: "render",
		Detail: fmt.Sprintf("%d frames/%d segments, frame hash %016x, segment hash %016x, mean luma %.2f, peak %s, %d recovered slots",
			first.Pixels.Frames, first.Audio.Chunks, first.FrameHash, first.AudioHash,
			first.Pixels.MeanLuma, render.FormatLoudness(first.Audio.PeakDBFS), recoveredSlots),
		Checks: checks,
	})
	return nil
}

// slotsFromSegments turns recovered RTP segments into render slots. When there
// are no segments (an empty stream) the render scenario still runs on an empty
// timeline.
func slotsFromSegments(segments []rtpkernel.Segment) []render.Slot {
	slots := make([]render.Slot, 0, len(segments))
	for _, segment := range segments {
		kind := render.SlotMedia
		switch segment.Kind {
		case rtpkernel.KindSilence:
			kind = render.SlotSilence
		}
		slots = append(slots, render.Slot{
			Kind:       kind,
			StartNS:    segment.StartNS,
			DurationNS: segment.DurationNS,
			Seq:        segment.FirstSeq,
		})
	}
	return slots
}

// ----------------------------------------------------------------- manifest

func init() { register("render", (*runner).runRender) }
