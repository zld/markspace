package recovery

import (
	"medcheck/internal/clock"
	"medcheck/internal/jitter"
)

// StrategyAudioSilencePLC is the explicit deterministic audio policy.
const StrategyAudioSilencePLC = "audio-silence-plc-v1"

// MediaEvent is a recovered, continuous media fragment.
type MediaEvent struct {
	Stream        clock.StreamID
	SSRC          uint32
	ExtSeq        uint64
	PTSNS         int64
	DurationNS    int64
	Offset        int64
	Payload       []byte
	Recovered     bool
	Strategy      string
	Keyframe      bool
	AwaitKeyframe bool
}

// Stats counts recovery actions.
type Stats struct {
	Recovered          int `json:"recovered"`
	AudioRecovered     int `json:"audio_recovered"`
	AudioUnrecoverable int `json:"audio_unrecoverable"`
	VideoGaps          int `json:"video_gaps"`
	KeyframeWaits      int `json:"keyframe_waits"`
	DroppedFrames      int `json:"dropped_frames"`
}

// Config defines explicit recovery thresholds.
type Config struct {
	// MaxAudioGapPackets limits PLC filling. Larger gaps are reported.
	MaxAudioGapPackets int
	// AudioFrameNS is used when a gap event does not carry duration.
	AudioFrameNS int64
	// Locate maps an extended sequence to its source input offset.
	Locate func(ssrc uint32, extSeq uint64) int64
}

// Recoverer fills allowed audio gaps and gates video until a keyframe.
type Recoverer struct {
	cfg     Config
	Stats   Stats
	Errors  []clock.MediaError
	await   map[uint32]bool
	waitErr map[uint32]clock.MediaError
}

// New creates a Recoverer.
func New(cfg Config) *Recoverer {
	if cfg.MaxAudioGapPackets <= 0 {
		cfg.MaxAudioGapPackets = 2
	}
	if cfg.AudioFrameNS <= 0 {
		cfg.AudioFrameNS = 20_000_000
	}
	return &Recoverer{
		cfg:     cfg,
		await:   make(map[uint32]bool),
		waitErr: make(map[uint32]clock.MediaError),
	}
}

// AwaitingMap returns a copy of video keyframe gates for checkpoints.
func (r *Recoverer) AwaitingMap() map[uint32]bool {
	out := make(map[uint32]bool, len(r.await))
	for k, v := range r.await {
		out[k] = v
	}
	return out
}

// RestoreAwaiting restores video keyframe gate state.
func (r *Recoverer) RestoreAwaiting(in map[uint32]bool) {
	r.await = make(map[uint32]bool, len(in))
	for k, v := range in {
		r.await[k] = v
	}
}

// AwaitingKeyframe reports video gate state for an SSRC.
func (r *Recoverer) AwaitingKeyframe(ssrc uint32) bool { return r.await[ssrc] }

// Handle consumes one ordered jitter event and returns zero or more media events.
func (r *Recoverer) Handle(ev jitter.Event) []MediaEvent {
	if ev.Kind == jitter.EventGap {
		return r.handleGap(ev)
	}
	if ev.Packet == nil {
		return nil
	}
	p := *ev.Packet
	if p.Stream == clock.StreamVideo && r.await[p.SSRC] {
		if !p.Keyframe {
			r.Stats.DroppedFrames++
			return nil
		}
		r.await[p.SSRC] = false
		r.Stats.KeyframeWaits++
	}
	return []MediaEvent{{
		Stream: p.Stream, SSRC: p.SSRC, ExtSeq: p.ExtSeq, PTSNS: p.PTSNS,
		DurationNS: p.DurationNS, Offset: p.Offset, Payload: p.Payload,
		Keyframe: p.Keyframe,
	}}
}

func (r *Recoverer) handleGap(ev jitter.Event) []MediaEvent {
	offset := r.locate(ev.SSRC, firstSeq(ev.Seqs))
	switch ev.Stream {
	case clock.StreamAudio:
		if len(ev.Seqs) > r.cfg.MaxAudioGapPackets {
			r.Stats.AudioUnrecoverable++
			r.Errors = append(r.Errors, clock.MediaError{
				Code: clock.CodeAudioGapUnrecoverable, Stream: ev.Stream, SSRC: ev.SSRC,
				Sequence: firstSeq(ev.Seqs), InputOffset: offset,
				Message: "audio gap exceeds PLC policy",
			})
			return nil
		}
		var out []MediaEvent
		for i, seq := range ev.Seqs {
			r.Stats.Recovered++
			r.Stats.AudioRecovered++
			r.Errors = append(r.Errors, clock.MediaError{
				Code: clock.CodeAudioRecovered, Stream: ev.Stream, SSRC: ev.SSRC,
				Sequence: seq, InputOffset: r.locate(ev.SSRC, seq),
				Source: StrategyAudioSilencePLC,
			})
			out = append(out, MediaEvent{
				Stream: ev.Stream, SSRC: ev.SSRC, ExtSeq: seq,
				PTSNS:      ev.PTSNS + int64(i)*r.cfg.AudioFrameNS,
				DurationNS: r.cfg.AudioFrameNS, Offset: r.locate(ev.SSRC, seq),
				Payload: []byte{0}, Recovered: true, Strategy: StrategyAudioSilencePLC,
			})
		}
		return out

	case clock.StreamVideo:
		r.Stats.VideoGaps++
		videoErr := clock.MediaError{
			Code: clock.CodeVideoAwaitKeyframe, Stream: ev.Stream, SSRC: ev.SSRC,
			Sequence: firstSeq(ev.Seqs), InputOffset: offset,
			Message: "video gap is not synthesized; waiting for keyframe",
		}
		r.await[ev.SSRC] = true
		r.waitErr[ev.SSRC] = videoErr
		r.Errors = append(r.Errors, videoErr)
		return []MediaEvent{{
			Stream: ev.Stream, SSRC: ev.SSRC, ExtSeq: firstSeq(ev.Seqs),
			PTSNS: ev.PTSNS, Offset: offset, AwaitKeyframe: true,
		}}
	default:
		return nil
	}
}

// Finish reports unresolved video keyframe gates.
func (r *Recoverer) Finish() []clock.MediaError {
	var out []clock.MediaError
	for ssrc, waiting := range r.await {
		if !waiting {
			continue
		}
		base := r.waitErr[ssrc]
		err := base
		err.Code = clock.CodeVideoUnrecoverable
		err.Message = "stream ended before next keyframe"
		r.Errors = append(r.Errors, err)
		out = append(out, err)
	}
	return out
}

func (r *Recoverer) locate(ssrc uint32, seq uint64) int64 {
	if r.cfg.Locate != nil {
		return r.cfg.Locate(ssrc, seq)
	}
	return -1
}

func firstSeq(seqs []uint64) uint64 {
	if len(seqs) == 0 {
		return 0
	}
	return seqs[0]
}
