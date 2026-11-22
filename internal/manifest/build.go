package manifest

import (
	"sort"

	"github.com/pion/rtp"

	"mediacore/internal/audio"
	"mediacore/internal/core"
	"mediacore/internal/timeline"
)

// Builder accumulates packets from segments.
type Builder struct {
	cfg    Config
	layout audio.Layout
	out    Manifest
	seq    uint16
	ts     int64 // absolute output tick position
	bytes  int64
	frames int64
}

// New starts a manifest with the given config and layout.
func New(cfg Config, layout audio.Layout) (*Builder, error) {
	if err := cfg.validate(layout); err != nil {
		return nil, err
	}
	if layout.NumChannels() != cfg.Channels {
		return nil, core.New(core.CodeParamConflict, core.StageManifest, false, "layout/channel mismatch")
	}
	return &Builder{cfg: cfg, layout: layout, out: Manifest{
		Version:    1,
		File:       cfg.File,
		SampleRate: cfg.SampleRate,
		Channels:   cfg.Channels,
		Layout:     layout.Name,
		TargetKbps: cfg.TargetKbps,
		Gaps:       []Gap{},
		Packets:    []Packet{},
	}}, nil
}

// AddSegment slices one contiguous buffer into packets. Packet timestamps come
// from StartTicks plus the segment's frame offset, converted with the shared
// rounding rule, never from accumulated packet durations.
func (b *Builder) AddSegment(seg Segment) error {
	if seg.Audio == nil {
		return nil
	}
	if err := seg.Audio.Validate(); err != nil {
		return core.Wrap(core.CodeUnrecoverable, core.StageManifest, false, "invalid segment audio", err)
	}
	const msPerPacket = 20
	packetMs := b.cfg.PacketMs
	if packetMs <= 0 {
		packetMs = msPerPacket
	}
	framesPerPacket := b.cfg.SampleRate * packetMs / 1000
	if framesPerPacket < 1 {
		framesPerPacket = 1
	}
	if _, err := payloadBytes(b.cfg, framesPerPacket); err != nil {
		return err
	}
	for start := 0; start < seg.Audio.Frames; start += framesPerPacket {
		end := start + framesPerPacket
		if end > seg.Audio.Frames {
			end = seg.Audio.Frames
		}
		frames := end - start
		chunk := sliceFrames(seg.Audio, start, end)
		payload, err := Shape(chunk, b.cfg.TargetKbps, b.cfg.SampleRate)
		if err != nil {
			return err
		}
		sub := Segment{Audio: chunk, StartTicks: seg.StartTicks + timeline.Ticks(start)}
		ts, err := b.packetTS(sub)
		if err != nil {
			return err
		}
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    b.cfg.PayloadType,
				SequenceNumber: b.seq,
				Timestamp:      ts,
				SSRC:           b.cfg.SSRC,
			},
			Payload: payload,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return core.Wrap(core.CodeUnrecoverable, core.StageManifest, false, "marshal rtp packet", err)
		}
		if len(raw) < 12+len(payload) {
			return core.New(core.CodeUnrecoverable, core.StageManifest, false, "short rtp packet")
		}
		startMs := timeline.ToMillis(sub.StartTicks, timeline.MustScale(int64(b.cfg.SampleRate)))
		b.out.Packets = append(b.out.Packets, Packet{
			Sequence:  b.seq,
			Timestamp: ts,
			StartMs:   startMs,
			Samples:   frames,
			Bytes:     len(payload),
			Wrapped:   b.wrapped(ts),
		})
		b.bytes += int64(len(payload))
		b.frames += int64(frames)
		b.seq++
		b.ts = int64(sub.StartTicks) + int64(frames)
	}
	return nil
}

// packetTS converts an output tick position into an RTP timestamp. The cast
// to uint32 is the defined wraparound; ordering is carried by SequenceNumber.
func (b *Builder) packetTS(seg Segment) (uint32, error) {
	if seg.StartTicks < 0 {
		return 0, core.New(core.CodeBadTimestamp, core.StageManifest, false, "negative packet timestamp")
	}
	return uint32(int64(seg.StartTicks)), nil
}

func (b *Builder) wrapped(ts uint32) bool {
	if len(b.out.Packets) == 0 {
		return false
	}
	return ts < b.out.Packets[len(b.out.Packets)-1].Timestamp
}

// SetGaps records output-timeline holes.
func (b *Builder) SetGaps(gaps []Gap) { b.out.Gaps = append([]Gap(nil), gaps...) }

// SetLoudness records measured levels.
func (b *Builder) SetLoudness(in, out float64, gainDB float64, limited bool) {
	b.out.InputLUFS = in
	b.out.OutputLUFS = out
	b.out.GainDB = gainDB
	b.out.GainLimited = limited
}

// SetStats records quality accounting.
func (b *Builder) SetStats(s audio.Stats) {
	b.out.Stats = Stats{
		InputFrames: s.InputFrames, OutputFrames: s.OutputFrames,
		ClippedSamples: s.ClippedSamples, SilentSamples: s.SilentSamples,
		Resets: s.Resets, TailFrames: s.TailFrames, Segments: s.Segments,
		PeakDBFS: s.PeakDBFS(), RMSDBFS: s.RMSDBFS(),
	}
	b.out.ClippedSamples = s.ClippedSamples
}

// AddStageError keeps a recoverable failure visible in the manifest.
func (b *Builder) AddStageError(msg string) { b.out.StageErrors = append(b.out.StageErrors, msg) }

// Finalize computes bitrate, alignment, duration, and summary digest. It is
// deterministic: identical packets always produce an identical digest.
func (b *Builder) Finalize(totalMs int64) (*Manifest, error) {
	m := &b.out
	m.PacketCount = len(m.Packets)
	m.PayloadBytes = b.bytes
	m.ValidMs = timeline.ToMillis(timeline.Ticks(b.frames), timeline.MustScale(int64(b.cfg.SampleRate)))
	m.TotalMs = totalMs
	if m.TotalMs < m.ValidMs {
		m.TotalMs = m.ValidMs
	}
	seconds := float64(m.ValidMs) / 1000
	if seconds <= 0 {
		return nil, core.New(core.CodeUnrecoverable, core.StageManifest, false, "no valid audio to send")
	}
	m.ActualKbps = float64(m.PayloadBytes) * 8 / seconds / 1000
	m.BitrateErrorPct = 100 * (m.ActualKbps - float64(m.TargetKbps)) / float64(m.TargetKbps)
	m.AlignmentMs = alignmentError(m.Packets, b.cfg.SampleRate)
	m.Summary = digest(m)
	return m, nil
}

// SummaryDigest is exposed for tests and repeated-run checks.
func SummaryDigest(m *Manifest) string { return digest(m) }

// SortPacketsBySequence is a stable invariant used by tests.
func SortPacketsBySequence(ps []Packet) {
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Sequence < ps[j].Sequence })
}
