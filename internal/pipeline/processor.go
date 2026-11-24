package pipeline

import (
	"medcheck/internal/checkpoint"
	"medcheck/internal/clock"
	"medcheck/internal/jitter"
	"medcheck/internal/output"
	"medcheck/internal/recovery"
	"medcheck/internal/rtp"
	"medcheck/internal/sample"
	"medcheck/internal/syncer"
)

// Processor owns the stateful media kernel stages.
type Processor struct {
	Sample *sample.Sample

	Ingestor *rtp.Ingestor
	Buffer   *jitter.Buffer
	Recovery *recovery.Recoverer
	Syncer   *syncer.Syncer

	AudioClock *clock.Timeline
	VideoClock *clock.Timeline

	AudioOut []output.Sample
	VideoOut []output.Sample

	ErrorCodes map[string]int
}

// NewProcessor wires all kernel stages for one generated sample.
func NewProcessor(s *sample.Sample) *Processor {
	in := rtp.NewIngestor()
	in.Register(sample.AudioSSRC, clock.StreamAudio)
	in.Register(sample.VideoSSRC, clock.StreamVideo)

	jb := jitter.New(jitter.DefaultConfig())
	jb.Register(sample.AudioSSRC, clock.StreamAudio)
	jb.Register(sample.VideoSSRC, clock.StreamVideo)

	offsetLookup := buildOffsetLookup(s)
	rec := recovery.New(recovery.Config{Locate: func(ssrc uint32, seq uint64) int64 {
		if m := offsetLookup[ssrc]; m != nil {
			return m[seq]
		}
		return -1
	}})

	audioClock, _ := clock.NewTimeline(clock.StreamAudio, sample.AudioSSRC, 48000, 0)
	videoClock, _ := clock.NewTimeline(clock.StreamVideo, sample.VideoSSRC, 90000, s.VideoOriginNS)
	if report, err := clock.ParseSenderReport(s.RTCP); err == nil {
		_, _ = videoClock.ApplyRTCP(report)
	}

	return &Processor{
		Sample: s, Ingestor: in, Buffer: jb, Recovery: rec, Syncer: syncer.New(),
		AudioClock: audioClock, VideoClock: videoClock, ErrorCodes: map[string]int{},
	}
}

func buildOffsetLookup(s *sample.Sample) map[uint32]map[uint64]int64 {
	out := map[uint32]map[uint64]int64{}
	for _, p := range s.All {
		if out[p.SSRC] == nil {
			out[p.SSRC] = map[uint64]int64{}
		}
		out[p.SSRC][p.ExtSeq] = p.Offset
	}
	return out
}

// ProcessPacket runs one generated wire packet through ingest -> buffer -> recovery -> sync.
func (p *Processor) ProcessPacket(pk *sample.Packet) {
	beforeErrs := len(p.Recovery.Errors)
	parsed, errs := p.Ingestor.Ingest(pk.Raw, pk.Offset)
	for _, e := range errs {
		p.ErrorCodes[string(e.Code)]++
	}
	if parsed.Kind == rtp.DispositionInvalid || parsed.Packet == nil {
		return
	}

	tl := p.AudioClock
	if pk.Stream == clock.StreamVideo {
		tl = p.VideoClock
	}
	pts := tl.Map(parsed.Packet.Timestamp)
	duration := tl.Duration(0)
	if pk.DurationNS > 0 {
		duration = pk.DurationNS
	}
	tp := jitter.TimedPacket{
		Stream: pk.Stream, SSRC: pk.SSRC, Seq: parsed.Packet.SequenceNumber,
		ExtSeq: parsed.ExtSequence, PTSNS: pts, DurationNS: duration,
		ArrivalNS: pk.ArrivalNS, Offset: pk.Offset, Payload: parsed.Packet.Payload,
		Keyframe: pk.Keyframe,
	}
	events, _, err := p.Buffer.Push(tp)
	if err != nil {
		p.ErrorCodes["JITTER_ERROR"]++
	}
	p.handleEvents(events)
	for _, e := range p.Recovery.Errors[beforeErrs:] {
		p.ErrorCodes[string(e.Code)]++
	}
}

func (p *Processor) handleEvents(events []jitter.Event) {
	for _, ev := range events {
		mediaEvents := p.Recovery.Handle(ev)
		for _, me := range mediaEvents {
			frame := p.Syncer.Push(me)
			sampleOut := output.Sample{
				PTSNS: frame.AdjustedPTSNS, DurationNS: frame.DurationNS,
				Payload: frame.Payload, Keyframe: frame.Keyframe,
			}
			if me.Stream == clock.StreamAudio {
				p.AudioOut = append(p.AudioOut, sampleOut)
			} else if me.Stream == clock.StreamVideo {
				p.VideoOut = append(p.VideoOut, sampleOut)
			}
		}
	}
}

// Finish flushes buffer gaps and reports unresolved recovery states.
func (p *Processor) Finish() {
	for ssrc, m := range map[uint32]uint64{sample.AudioSSRC: maxExt(p.Sample, sample.AudioSSRC), sample.VideoSSRC: maxExt(p.Sample, sample.VideoSSRC)} {
		p.Buffer.SetFinalSeq(ssrc, m)
	}
	events := p.Buffer.Flush()
	p.handleEvents(events)
	for _, e := range p.Recovery.Finish() {
		p.ErrorCodes[string(e.Code)]++
	}
}

func maxExt(s *sample.Sample, ssrc uint32) uint64 {
	var max uint64
	for _, p := range s.All {
		if p.SSRC == ssrc && p.ExtSeq > max {
			max = p.ExtSeq
		}
	}
	return max
}

// Document returns the output document with validated adapter metadata.
func (p *Processor) Document() output.Document {
	return output.Document{
		AudioSamples: p.AudioOut, Audio: p.Sample.Audio,
		VideoSamples: p.VideoOut, Video: p.Sample.Video,
	}
}

// Checkpoint captures state after boundary processed packets.
func (p *Processor) Checkpoint(boundary int) checkpoint.State {
	rtpMax, rtpSeen := p.Ingestor.SnapshotSequences()
	syn := p.Syncer.Export()
	st := checkpoint.State{
		Version: checkpoint.Version, Boundary: boundary,
		RTPMax: rtpMax, RTPSeen: rtpSeen,
		JitterState: p.Buffer.Snapshot(),
		Syncer: checkpoint.SyncerState{
			LatestRaw: stringMap(syn.LatestRaw), Offsets: stringMap(syn.Offsets),
			LastAdjusted: stringMap(syn.LastAdjusted), HaveAdjusted: boolStringMap(syn.HaveAdjusted),
			Frames: syn.Stats.Frames, Corrections: syn.Stats.Corrections,
			MaxDriftNS: syn.Stats.MaxDriftNS, AvDriftMS: syn.Stats.AvDriftMS,
		},
		Clocks: map[string]checkpoint.ClockState{
			string(clock.StreamAudio): clockToCheckpoint(p.AudioClock.Export()),
			string(clock.StreamVideo): clockToCheckpoint(p.VideoClock.Export()),
		},
		AwaitKeyframe: p.Recovery.AwaitingMap(),
		Recovered:     p.Recovery.Stats.Recovered,
		IngestStats:   p.Ingestor.Stats,
		ErrorCodes:    p.ErrorCodes,
		AudioSamples:  toCheckpointSamples(p.AudioOut),
		VideoSamples:  toCheckpointSamples(p.VideoOut),
	}
	return st
}

// Restore replaces processor state from a valid checkpoint.
func (p *Processor) Restore(st checkpoint.State) {
	p.Ingestor.Stats = st.IngestStats
	p.Ingestor.RestoreSequences(st.RTPMax, st.RTPSeen)
	if st.JitterState != nil {
		p.Buffer.Restore(st.JitterState)
	}
	p.Syncer.Restore(syncer.State{
		LatestRaw: clockIDMap(st.Syncer.LatestRaw), Offsets: clockIDMap(st.Syncer.Offsets),
		LastAdjusted: clockIDMap(st.Syncer.LastAdjusted), HaveAdjusted: clockBoolMap(st.Syncer.HaveAdjusted),
		Stats: syncer.Stats{Frames: st.Syncer.Frames, Corrections: st.Syncer.Corrections,
			MaxDriftNS: st.Syncer.MaxDriftNS, AvDriftMS: st.Syncer.AvDriftMS},
	})
	if c, ok := st.Clocks[string(clock.StreamAudio)]; ok {
		p.AudioClock.Restore(clockFromCheckpoint(c))
	}
	if c, ok := st.Clocks[string(clock.StreamVideo)]; ok {
		p.VideoClock.Restore(clockFromCheckpoint(c))
	}
	p.Recovery.RestoreAwaiting(st.AwaitKeyframe)
	p.Recovery.Stats.Recovered = st.Recovered
	p.ErrorCodes = st.ErrorCodes
	if p.ErrorCodes == nil {
		p.ErrorCodes = map[string]int{}
	}
	p.AudioOut = fromCheckpointSamples(st.AudioSamples)
	p.VideoOut = fromCheckpointSamples(st.VideoSamples)
}
