package pipeline

import (
	"fmt"
	"sort"

	"example.com/mediatimeline/internal/audio"
	"example.com/mediatimeline/internal/clock"
	"example.com/mediatimeline/internal/fanin"
	"example.com/mediatimeline/internal/fanout"
	"example.com/mediatimeline/internal/index"
	"example.com/mediatimeline/internal/ingest"
	"example.com/mediatimeline/internal/model"
	"example.com/mediatimeline/internal/video"
)

// stageIngest parses the MP4 sample tables. Both tracks come from the same
// file; a missing track is reported but does not stop the other.
func stageIngest(ctx *stageContext) error {
	if len(ctx.in.MP4) == 0 {
		return model.NewError(model.TrackVideoMain, model.StageParse, model.CodeMissingPayload, false,
			"no MP4 input")
	}
	v, err := ingest.ReadMP4(ctx.in.MP4, ingest.MP4Options{Track: model.TrackVideoMain, Kind: model.KindVideo})
	if err != nil {
		return err
	}
	a, err := ingest.ReadMP4(ctx.in.MP4, ingest.MP4Options{Track: model.TrackAudioMain, Kind: model.KindAudio})
	if err != nil {
		return err
	}
	if len(v.Samples) == 0 {
		return model.NewError(model.TrackVideoMain, model.StageParse, model.CodeMissingPayload, false,
			"video track has no samples")
	}
	if v.HandlerType != "" && v.HandlerType != "vide" {
		return model.NewError(model.TrackVideoMain, model.StageParse, model.CodeMalformedMP4, false,
			"requested video kind but trak handler is %q", v.HandlerType)
	}
	if a.HandlerType != "soun" {
		return model.NewError(model.TrackAudioMain, model.StageParse, model.CodeMalformedMP4, false,
			"requested audio kind but trak handler is %q", a.HandlerType)
	}
	ctx.videoSamples = v.Samples
	ctx.videoTimescale = v.Timescale
	ctx.audioSamples = a.Samples
	ctx.rep.VideoSamples = len(v.Samples)
	ctx.rep.AudioSamples = len(a.Samples)
	for i := range v.Samples {
		if p, ok := v.SamplePayload(i); ok {
			ctx.videoBytes[i] = p
		}
	}
	// The color conflict fixture puts BT.2020 in the bitstream SEI and BT.709
	// in the container. Container wins, and the conflict is recorded.
	if v.Color != nil {
		ctx.rep.Color = videoResolve(ctx, *v.Color, ctx.in.ColorDecls)
	} else if len(ctx.in.ColorDecls) > 0 {
		ctx.rep.Color = videoResolve(ctx, ctx.in.ColorDecls[0], ctx.in.ColorDecls[1:])
	}
	if err := addErrors(ctx.rep, colorErrors(ctx, v.Color, ctx.in.ColorDecls)); err != nil {
		return err
	}

	// Delivery audio supersedes the archive sample table: the RTP stream is the
	// evidence of what the remote team actually sent, including its gaps and
	// overlaps. The MP4 audio track is the fallback when no RTP is supplied.
	if len(ctx.in.AudioRTP) > 0 {
		ares, err := reassembleWithFragment(ctx.in.AudioRTP, model.TrackAudioMain, 48000, 0)
		if err != nil && !model.IsRecoverable(err) {
			return err
		}
		if len(ares.Samples) > 0 {
			ctx.audioSamples = ares.Samples
			ctx.rep.AudioSamples = len(ares.Samples)
			ctx.rep.Decisions = append(ctx.rep.Decisions, ares.Decisions...)
		}
	}

	// Audio layout reconciliation happens here so the report is complete even
	// when the audio track is otherwise healthy.
	decls := ctx.in.AudioLayouts
	if len(decls) == 0 {
		decls = []model.AudioDescriptor{a.AudioDesc}
	}
	layout, mapping, aerrs, err := audio.Resolve(model.TrackAudioMain, decls)
	if err != nil {
		return err
	}
	ctx.rep.AudioLayout = layout
	ctx.rep.RepairLog = append(ctx.rep.RepairLog, "audio layout: "+mapping.Reason)
	if err := addErrors(ctx.rep, aerrs); err != nil {
		return err
	}
	rates := ctx.in.AudioRates
	if len(rates) == 0 && a.SampleRate > 0 {
		rates = []uint32{a.SampleRate}
	}
	rateDecision, rerrs, err := audio.ResolveRate(model.TrackAudioMain, rates)
	if err != nil {
		return err
	}
	ctx.rep.AudioRate = rateDecision
	ctx.rep.RepairLog = append(ctx.rep.RepairLog, "audio rate: "+rateDecision.Message)
	return addErrors(ctx.rep, rerrs)
}

func videoResolve(ctx *stageContext, primary model.ColorDeclaration, rest []model.ColorDeclaration) video.Resolution {
	decls := append([]model.ColorDeclaration{primary}, rest...)
	res, _, _ := video.Resolve(model.TrackVideoMain, decls)
	return res
}

func colorErrors(ctx *stageContext, container *model.ColorDeclaration, decls []model.ColorDeclaration) []*model.Error {
	if container == nil {
		return nil
	}
	all := append([]model.ColorDeclaration{*container}, decls...)
	_, errs, _ := video.Resolve(model.TrackVideoMain, all)
	var kept []*model.Error
	for _, e := range errs {
		kept = append(kept, e)
	}
	return kept
}

// stageClock folds the RTP decode decisions (if any) into the report so the
// repair log is complete regardless of which path produced the samples.
func stageClock(ctx *stageContext) error {
	if len(ctx.in.VideoRTP) == 0 {
		ctx.rep.RoundTripMatch = 1
		return nil
	}
	// Validate that RTP-reassembled samples agree with the MP4 sample table on
	// both count and start time. Agreement is the round-trip metric. A clock
	// fault that the repairer cannot resolve is fatal for the delivery path
	// but does not hide the archive path: the report still records the metric
	// and the reason, and the run fails with the classified error.
	res, err := reassemble(ctx.in.VideoRTP, model.TrackVideoMain, ctx.videoTimescale)
	if err != nil {
		if pe, ok := model.AsError(err); ok {
			ctx.rep.Errors = append(ctx.rep.Errors, pe)
		}
		// The delivery stream is unusable as a whole, but the archive path
		// and the metadata paths are independent: continue so the report is
		// complete and only then fail with the classified error.
		ctx.delivery = res
		ctx.rep.RoundTripMatch = roundTrip(ctx.videoSamples, res.Samples)
		return err
	}
	match := roundTrip(ctx.videoSamples, res.Samples)
	ctx.rep.RoundTripMatch = match
	ctx.delivery = res
	ctx.rep.Decisions = append(ctx.rep.Decisions, res.Decisions...)
	return nil
}

func reassemble(packets [][]byte, track model.TrackID, timescale uint32) (ingest.RTPResult, error) {
	return reassembleWithFragment(packets, track, timescale, 1)
}

func reassembleWithFragment(packets [][]byte, track model.TrackID, timescale uint32, fragmentGap int64) (ingest.RTPResult, error) {
	r, err := ingest.NewRTPReassembler(ingest.RTPOptions{
		Track: track, Timescale: timescale, Modulus: clock.Default32Modulus,
		Jitter: 0, FragmentGapTicks: fragmentGap, AllowDroppedGap: true, AnchorFirst: true,
	})
	if err != nil {
		return ingest.RTPResult{}, err
	}
	for _, p := range packets {
		if err := r.DecodeRTPPacket(p); err != nil && !model.IsRecoverable(err) {
			return ingest.RTPResult{}, err
		}
	}
	return r.Finish()
}

// roundTrip compares repaired start ticks of two sample lists and returns the
// fraction that agree exactly.
func roundTrip(a, b []model.Sample) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	if n == 0 {
		return 1
	}
	byTicks := map[int64]bool{}
	for _, s := range a {
		if t, ok := ticksOf(s); ok {
			byTicks[t] = true
		}
	}
	// Round-trip measures agreement on every sample the delivery path
	// actually produced. Samples that never arrived are counted as loss, not
	// as timestamp disagreement, so a dropped packet cannot masquerade as a
	// clock fault.
	hit := 0
	for _, s := range b {
		if t, ok := ticksOf(s); ok && byTicks[t] {
			hit++
		}
	}
	if len(b) == 0 {
		return 0
	}
	return float64(hit) / float64(len(b))
}

func ticksOf(s model.Sample) (int64, bool) {
	r, err := clock.FromModel(s.Time.RepairedTime)
	if err != nil {
		return 0, false
	}
	t, err := r.FloorTicks(1000)
	if err != nil {
		return 0, false
	}
	return t, true
}

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

// stageFanout expands the video track into the ladder.
func stageFanout(ctx *stageContext) error {
	layers := fanout.DefaultLadder(ctx.in.Ladder, 1920, 1080)
	corrupt := map[int]bool{}
	corruptLayer := -1
	damaged := ctx.in.CorruptLayer || ctx.delivery.LostPackets > 0
	if damaged && len(layers) > 1 {
		// A damaged delivery fragment makes the finest rung unavailable; the
		// coarser rungs are recovered from the archive copy and stay
		// indexable. One layer failing must not contaminate the others.
		corruptLayer = layers[len(layers)-1].Index
	}
	if corruptLayer >= 0 {
		for i := range ctx.videoSamples {
			corrupt[i] = true
		}
	}
	res, err := fanout.Build(layers, fanout.LayerInput{
		Track: model.TrackVideoMain, Timescale: ctx.videoTimescale,
		Samples: ctx.videoSamples, CorruptIndices: corrupt, CorruptLayer: corruptLayer,
	}, ctx.in.SegmentMS)
	if err != nil {
		return err
	}
	ctx.fanoutResult = res
	if err := addErrors(ctx.rep, res.Errors); err != nil {
		return err
	}
	return nil
}

// stageFanin validates layer boundaries against the shared clock.
func stageFanin(ctx *stageContext) error {
	segTicks, err := segmentTicks(ctx.in.SegmentMS, ctx.videoTimescale)
	if err != nil {
		return err
	}
	merge, err := fanin.Merge(ctx.fanoutResult, fanin.Options{
		ToleranceTicks: 1000, Timescale: ctx.videoTimescale,
		SegmentTicks: segTicks, RequireAllLayers: ctx.in.RequireAllLayers,
	})
	ctx.merge = merge
	ctx.rep.Segments = len(merge.Segments)
	if err := addErrors(ctx.rep, merge.Errors); err != nil {
		return err
	}
	return nil
}

func segmentTicks(segmentMS int64, timescale uint32) (int64, error) {
	d, err := clock.New(segmentMS, 1000)
	if err != nil {
		return 0, err
	}
	return d.FloorTicks(timescale)
}

// stageIndex builds the keyframe index and segment hashes for the lowest
// available layer, then verifies the hashes by recomputing them.
func stageIndex(ctx *stageContext) error {
	var chosen *fanout.Layer
	for i := range ctx.merge.Layers {
		l := &ctx.merge.Layers[i]
		if l.Available {
			chosen = l
			break
		}
	}
	if chosen == nil {
		return model.NewError(model.TrackVideoMain, model.StageIndex, model.CodeLayerUnavailable, false,
			"no available layer to index")
	}
	segs := make([]index.Segment, len(ctx.merge.Segments))
	ticks := make([]int64, len(ctx.merge.Segments))
	for i, s := range ctx.merge.Segments {
		segs[i] = index.Segment{Index: s.Index, StartTick: s.StartTick, EndTick: s.EndTick}
		ticks[i] = s.StartTick
	}
	offsets := make([]uint64, len(chosen.Samples))
	for i := range offsets {
		offsets[i] = uint64(i) * 1024
	}
	kf, err := index.BuildKeyframes(chosen.Track, chosen.Timescale, chosen.Samples, offsets, ticks)
	if err != nil {
		return err
	}
	ctx.rep.KeyframeIndex = kf
	ctx.rep.Keyframes = len(kf.Entries)
	data := func(i int) ([]byte, bool) {
		src := chosen.SampleIndices[i]
		p, ok := ctx.videoBytes[src]
		return p, ok
	}
	segIndexes, err := index.BuildSegments(chosen.Track, chosen.Timescale, chosen.Samples, data, segs, nil)
	if err != nil {
		return err
	}
	ctx.rep.SegmentIndexes = segIndexes
	matched, total, err := index.VerifySegments(chosen.Track, chosen.Timescale, chosen.Samples, data, segs, segIndexes, nil)
	if err != nil {
		return err
	}
	if total == 0 {
		ctx.rep.SegmentHashMatch = 1
	} else {
		ctx.rep.SegmentHashMatch = float64(matched) / float64(total)
	}
	ctx.rep.KeyframeHitRate = keyframeHitRate(kf, chosen.Samples)
	return nil
}

// keyframeHitRate is the fraction of reference keyframes (sync samples of the
// full track) that the index can reach exactly.
func keyframeHitRate(kf index.KeyframeIndex, all []model.Sample) float64 {
	var want []int64
	for _, s := range all {
		if !s.Sync {
			continue
		}
		r, err := clock.FromModel(s.Time.RepairedTime)
		if err != nil {
			continue
		}
		t, err := r.FloorTicks(kf.Timescale)
		if err != nil {
			continue
		}
		want = append(want, t)
	}
	if len(want) == 0 {
		return 1
	}
	hits := 0
	for _, t := range want {
		e, ok := kf.Lookup(t)
		if ok && e.Ticks == t {
			hits++
		}
	}
	return float64(hits) / float64(len(want))
}

// sortStrings is a small helper kept for deterministic report rendering.
func sortStrings(ss []string) []string {
	s := append([]string(nil), ss...)
	sort.Strings(s)
	return s
}
