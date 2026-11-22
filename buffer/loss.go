package buffer

import "mediacore"

// RecoveryConfig parameterizes deterministic silence recovery.
type RecoveryConfig struct {
	Track    mediacore.TrackID
	Channels int
	Rate     int
	// BlockSamples is the unique, fixed block size in sample frames. Recovery
	// is only legal when a gap's sample count is uniquely determined by the
	// neighbouring blocks and this scale.
	BlockSamples int
}

// LossRecovery fills deterministic silence into gaps whose sample count is
// uniquely determined by the surrounding PCM blocks. It never fabricates
// waveform data, never guesses ambiguous gap sizes and rejects any layout
// change with a classified error carrying track, stage and offset.
type LossRecovery struct {
	cfg RecoveryConfig
}

// NewLossRecovery builds a recovery helper bound to one audio track.
func NewLossRecovery(cfg RecoveryConfig) *LossRecovery {
	return &LossRecovery{cfg: cfg}
}

// RecoverSilence attempts to fill one gap event between prev (the last block
// before the gap) and next (the first block after it). It returns the
// synthesized silent PCM event, or a classified error when the gap is not
// uniquely recoverable or the neighbouring blocks violate the layout.
func (r *LossRecovery) RecoverSilence(gap, prev, next mediacore.Event) (mediacore.Event, error) {
	if gap.Kind != mediacore.KindGap {
		return mediacore.Event{}, mediacore.NewError(mediacore.CatLayout, r.cfg.Track,
			"recover.recover", int64(gap.Seq), "event is not a gap record")
	}
	for _, e := range []mediacore.Event{prev, next} {
		if e.Kind != mediacore.KindPCM {
			return mediacore.Event{}, mediacore.NewError(mediacore.CatGap, r.cfg.Track,
				"recover.anchors", int64(gap.Seq), "gap is not bounded by PCM blocks on both sides")
		}
		if err := r.checkLayout(e); err != nil {
			return mediacore.Event{}, err
		}
	}
	// The gap size must be uniquely determined by the fixed block scale.
	if gap.Samples != r.cfg.BlockSamples || prev.Samples != r.cfg.BlockSamples ||
		next.Samples != r.cfg.BlockSamples {
		return mediacore.Event{}, mediacore.NewError(mediacore.CatGap, r.cfg.Track,
			"recover.uniqueness", int64(gap.Seq),
			"gap size not uniquely determined by neighbouring block length and sample scale")
	}
	// Silence duration must match the hole on the integer timeline exactly.
	holeStart := prev.Time + prev.Duration()
	holeEnd := next.Time
	wantDur := int64(r.cfg.BlockSamples) * mediacore.Second / int64(r.cfg.Rate)
	if holeEnd-holeStart != wantDur {
		return mediacore.Event{}, mediacore.NewError(mediacore.CatOverlap, r.cfg.Track,
			"recover.timeline", holeStart, "neighbouring blocks do not leave a single-block hole")
	}
	out := gap
	out.Kind = mediacore.KindPCM
	out.Time = holeStart
	out.OrigTime = holeStart
	out.Samples = r.cfg.BlockSamples
	out.Channels = r.cfg.Channels
	out.Rate = r.cfg.Rate
	out.Payload = make([]byte, r.cfg.BlockSamples*r.cfg.Channels*2) // 16-bit PCM, zero = silence
	out.Reason = "recovered silence"
	return out, nil
}

// checkLayout rejects channel-count changes and payload lengths that do not
// divide evenly into 16-bit channel samples.
func (r *LossRecovery) checkLayout(e mediacore.Event) error {
	if e.Channels != r.cfg.Channels {
		return mediacore.NewError(mediacore.CatLayout, e.Track, "recover.channels",
			int64(e.Seq), "channel count changed mid-stream")
	}
	if e.Rate != r.cfg.Rate {
		return mediacore.NewError(mediacore.CatLayout, e.Track, "recover.rate",
			int64(e.Seq), "sample rate changed mid-stream")
	}
	frame := e.Channels * 2
	if frame == 0 || len(e.Payload)%frame != 0 {
		return mediacore.NewError(mediacore.CatLayout, e.Track, "recover.payload",
			int64(len(e.Payload)), "block length not divisible by channel frame size")
	}
	return nil
}

// CheckContinuity validates two adjacent emitted PCM blocks. It reports a
// classified overlap/layout error instead of silently overwriting data.
func CheckContinuity(prev, next mediacore.Event) error {
	if prev.Kind != mediacore.KindPCM || next.Kind != mediacore.KindPCM {
		return nil
	}
	if next.Channels != prev.Channels {
		return mediacore.NewError(mediacore.CatLayout, next.Track, "continuity.channels",
			int64(next.Seq), "channel count changed between adjacent blocks")
	}
	frame := next.Channels * 2
	if frame == 0 || len(next.Payload)%frame != 0 {
		return mediacore.NewError(mediacore.CatLayout, next.Track, "continuity.payload",
			int64(next.Seq), "block length not divisible by channel frame size")
	}
	if next.Time < prev.Time+prev.Duration() {
		return mediacore.NewError(mediacore.CatOverlap, next.Track, "continuity.overlap",
			next.Time, "PCM block overlaps the previous block on the timeline")
	}
	return nil
}
