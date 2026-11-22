package pipeline

import (
	"fmt"

	"example.com/mediatimeline/internal/audio"
	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/ingest"
	"example.com/mediatimeline/internal/model"
	"example.com/mediatimeline/internal/video"
)

// stageRecoverVideo validates keyframes and adds channel-noise-free video
// recovery output to the report.
func stageRecoverVideo(ctx *stageContext) error {
	candidates := make([]int, 0)
	for i, s := range ctx.videoSamples {
		if s.Sync {
			candidates = append(candidates, i)
		}
	}
	check, errs, err := video.ValidateKeyframes(model.TrackVideoMain, ctx.videoSamples, candidates, ctx.videoTimescale)
	if err != nil {
		return err
	}
	ctx.rep.RepairLog = append(ctx.rep.RepairLog,
		fmt.Sprintf("video: %d candidate keyframes, %d off-boundary", len(candidates), check.OffBoundary))
	return addErrors(ctx.rep, errs)
}

// stageRecoverAudio reconciles audio frames against the nominal 1024-tick
// grid at 48 kHz, then reports the decisions.
func stageRecoverAudio(ctx *stageContext) error {
	if len(ctx.audioSamples) == 0 {
		return model.NewError(model.TrackAudioMain, model.StageAudio, model.CodeMissingPayload, false,
			"audio track has no samples")
	}
	opts := audio.ReconcileOptions{
		Track: model.TrackAudioMain, FrameTicks: 1024, Timescale: 48000,
		MaxGapTicks: 48000, FullFrameBytes: 192,
	}
	frames := make([]audio.Frame, 0, len(ctx.audioSamples))
	for _, s := range ctx.audioSamples {
		dur := s.Duration
		if dur.Den == 0 || (dur.Num == 0 && s.Size == 0) {
			// RTP audio carries a timestamp but no nominal duration; the
			// grid defines one frame per 1024 ticks at 48 kHz.
			r, err := clock.FromTicks(1024, 48000)
			if err != nil {
				return err
			}
			dur = clock.ToModel(r)
		}
		frames = append(frames, audio.Frame{Index: s.Index, Start: s.Time.RepairedTime, Duration: dur, Size: int(s.Size)})
	}
	res, err := audio.Reconcile(frames, opts)
	if err != nil {
		return err
	}
	for _, d := range res.Decisions {
		ctx.rep.AudioDecisions = append(ctx.rep.AudioDecisions, d)
		ctx.rep.RepairLog = append(ctx.rep.RepairLog, fmt.Sprintf("audio %s #%d: %s", d.Kind, d.Index, d.Message))
	}
	if err := addErrors(ctx.rep, res.Errors); err != nil {
		return err
	}
	ctx.rep.AudioSamples = len(res.Frames)
	return nil
}

// stageRecoverEvents parses every metadata carrier on its own clock.
func stageRecoverEvents(ctx *stageContext) error {
	res, err := ingest.ParseEvents(ingest.EventInput{
		WebVTT: ctx.in.WebVTT, Chapters: ctx.in.Chapters, ID3: ctx.in.ID3, ID3Ticks: ctx.in.ID3Ticks,
		Emsg: ctx.in.Emsg, SEI: [][]byte{ctx.in.SEI},
	})
	if err != nil {
		return err
	}
	ctx.events = res.Events
	ctx.rep.EventCount = len(res.Events)
	ctx.rep.Decisions = append(ctx.rep.Decisions, res.Decisions...)
	var memory int64
	for _, e := range res.Events {
		memory += int64(len(e.ID) + len(e.Text) + len(e.Scheme) + len(e.Value) + len(e.Payload) + 128)
	}
	ctx.rep.EventMemoryBytes = memory
	if memory > ctx.limits.MaxEventMemoryBytes {
		return model.NewError("", model.StageValidation, model.CodeLimitExceeded, false,
			"event index needs %d bytes, above the %d-byte limit", memory, ctx.limits.MaxEventMemoryBytes)
	}
	if err := addErrors(ctx.rep, res.Errors); err != nil {
		return err
	}
	ctx.rep.RepairLog = append(ctx.rep.RepairLog, fmt.Sprintf("events: %d recovered", len(res.Events)))
	return nil
}
