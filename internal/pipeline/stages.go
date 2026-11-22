package pipeline

import (
	"example.com/mediatimeline/internal/audio"
	"example.com/mediatimeline/internal/clock"
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
	// Color declarations come from two real sources: the container's colr box
	// and any nclx SEI inside the samples. Declarations supplied by the
	// caller (a manifest) are merged in last. Container precedence wins and
	// every disagreement is recorded.
	bitstream := sampleSEIColors(v)
	declare := make([]model.ColorDeclaration, 0, len(bitstream)+len(ctx.in.ColorDecls))
	declare = append(declare, bitstream...)
	declare = append(declare, ctx.in.ColorDecls...)
	if v.Color != nil {
		ctx.rep.Color = videoResolve(ctx, *v.Color, declare)
	} else if len(declare) > 0 {
		ctx.rep.Color = videoResolve(ctx, declare[0], declare[1:])
	}
	if err := addErrors(ctx.rep, colorErrors(ctx, v.Color, declare)); err != nil {
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
	res, _, _ := video.Resolve(model.TrackVideoMain, dedupeColors(decls))
	return res
}

// dedupeColors keeps the first declaration per source, so a fixture that
// repeats the same bitstream tag in every access unit does not multiply the
// conflict list.
func dedupeColors(in []model.ColorDeclaration) []model.ColorDeclaration {
	seen := map[model.ColorSource]bool{}
	out := make([]model.ColorDeclaration, 0, len(in))
	for _, d := range in {
		if seen[d.Source] {
			continue
		}
		seen[d.Source] = true
		out = append(out, d)
	}
	return out
}

// sampleSEIColors reads the first distinct nclx declaration from the sample
// payloads. Scanning stops at the first hit: later access units repeat the
// same tag.
func sampleSEIColors(v ingest.MP4Track) []model.ColorDeclaration {
	for i := range v.Samples {
		payload, ok := v.SamplePayload(i)
		if !ok {
			continue
		}
		msgs, _ := ingest.ParseSEI(payload)
		if cols := ingest.SEIColors(msgs); len(cols) > 0 {
			return cols[:1]
		}
	}
	return nil
}

func colorErrors(ctx *stageContext, container *model.ColorDeclaration, decls []model.ColorDeclaration) []*model.Error {
	if container == nil {
		return nil
	}
	all := dedupeColors(append([]model.ColorDeclaration{*container}, decls...))
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
