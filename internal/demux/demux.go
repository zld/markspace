// Package demux reassembles transport packets into media samples, assigns
// them to unified-timeline segments, and scans each sample for codec
// boundaries. It never alters sample bytes.
package demux

import (
	"sort"

	"mediadist/internal/codec"
	"mediadist/internal/merr"
	"mediadist/internal/timeline"
	"mediadist/internal/transport"
)

// Codec identifies the sample payload framing.
type Codec string

const (
	CodecH264 Codec = "h264"
	CodecH265 Codec = "h265"
	CodecAV1  Codec = "av1"
	CodecOpus Codec = "opus"
	CodecRaw  Codec = "raw"
)

// IsVideo reports whether the codec is one of the scanned video codecs.
func (c Codec) IsVideo() bool {
	return c == CodecH264 || c == CodecH265 || c == CodecAV1
}

// StreamSpec describes one input stream and its clock.
type StreamSpec struct {
	Stream int
	TrackID uint32
	Codec   Codec
	// Timescale is the RTP clock rate in ticks per second.
	Timescale uint32
	// WrapBits is 32 for standard RTP timestamps.
	WrapBits uint
	// ClockStart is added to the first expanded timestamp so a non-zero
	// origin can be preserved explicitly.
	ClockStart int64
	// FrameDuration is the fallback duration in ticks for the last sample.
	FrameDuration int64
	// SegmentNS is the unified segment length in nanoseconds.
	SegmentNS int64
	// OriginNS shifts the unified segment grid.
	OriginNS int64
}

// Sample is one reassembled access unit.
type Sample struct {
	Index   int
	Stream  int
	TrackID uint32
	Codec   Codec
	PTS     int64 // ticks in the stream timescale
	PTSNS   int64 // unified nanoseconds, exact conversion
	Dur     int64 // ticks
	DurNS   int64
	Data    []byte
	Keyframe bool
	Damaged bool
	// MissingPackets counts sequence gaps observed inside this sample.
	MissingPackets int
	// Incomplete is true when the marker/last packet was absent.
	Incomplete bool
	// RawTimestamp is the unexpanded 32-bit RTP timestamp. It is the stable
	// identity external indexes use, because it survives reordering and loss
	// of other samples.
	RawTimestamp uint32
	SeqFirst     uint16
	SeqLast      uint16
	Segment    int
	Scan       codec.ScanResult
}

// Hashable reports whether the sample can participate in a byte comparison.
func (s Sample) Hashable() bool { return len(s.Data) > 0 }

// Stream is the demuxed output for one input stream.
type Stream struct {
	Spec    StreamSpec
	Samples []Sample
	Errors  []*merr.Error
	// Clock is retained so evidence can render origin and rate.
	Clock *timeline.Clock
	// ScannedDamages counts damaged byte spans found by the codec scanner.
	ScannedDamages int
}

// Result is the full demux output.
type Result struct {
	Streams []*Stream
	Errors  []*merr.Error
}

// Demux groups packets into access units. Packets must already be deduplicated
// and in sequence order for their stream. gaps carries the committed and
// pending sequence gaps from transport so damage can be attributed exactly.
func Demux(specs []StreamSpec, byStream map[int][]*transport.Packet,
	gaps map[int][]transport.SeqSpan, scans codec.ScanLimits) (*Result, error) {
	res := &Result{}
	if scans.MaxUnits == 0 {
		scans = codec.DefaultScanLimits()
	}
	for _, spec := range specs {
		clock, err := timeline.NewClock(spec.Timescale, spec.WrapBits)
		if err != nil {
			return nil, err
		}
		clock.Start = spec.ClockStart
		st := &Stream{Spec: spec, Clock: clock}
		packets := byStream[spec.Stream]
		if len(packets) > 0 {
			st.Samples, st.Errors = demuxStream(spec, clock, packets, gaps[spec.Stream], scans)
			for i := range st.Samples {
				st.ScannedDamages += len(st.Samples[i].Scan.Damaged)
			}
		}
		res.Streams = append(res.Streams, st)
		res.Errors = append(res.Errors, st.Errors...)
	}
	return res, nil
}

// demuxStream performs the per-stream reassembly.
func demuxStream(spec StreamSpec, clock *timeline.Clock, packets []*transport.Packet,
	gaps []transport.SeqSpan, scans codec.ScanLimits) ([]Sample, []*merr.Error) {
	var samples []Sample
	var errs []*merr.Error
	gapSet := make(map[int64]struct{}, len(gaps))
	for _, g := range gaps {
		for s := g.First; s <= g.Last; s++ {
			gapSet[s] = struct{}{}
		}
	}

	// Extended sequence tracking detects leading and internal holes.
	primed := false
	var ref int64

	i := 0
	for i < len(packets) {
		j := i + 1
		ts := packets[i].Timestamp
		for j < len(packets) && packets[j].Timestamp == ts {
			j++
		}
		group := packets[i:j]
		firstExt := extend(group[0].Seq, ref, primed)
		if !primed {
			ref, primed = firstExt, true
		}
		lastExt := extend(group[len(group)-1].Seq, ref, primed)
		if lastExt > ref {
			ref = lastExt
		}

		// Missing packets inside this access unit are counted from the
		// committed loss set, which is authoritative: reordering must not look
		// like loss. Missing packets between this group and the previous one
		// belong to the previous sample's tail and are not charged twice.
		missing := 0
		for gs := firstExt; gs <= lastExt; gs++ {
			if _, ok := gapSet[gs]; ok {
				missing++
			}
		}
		incomplete := !group[len(group)-1].Marker

		pts := clock.Expand(ts)
		ns := clock.ToNS(pts)
		data := make([]byte, 0, totalLen(group))
		for _, p := range group {
			data = append(data, p.Payload...)
		}
		s := Sample{
			Index:          len(samples),
			Stream:         spec.Stream,
			TrackID:        spec.TrackID,
			Codec:          spec.Codec,
			PTS:            pts,
			PTSNS:          ns,
			Data:           data,
			MissingPackets: missing,
			Incomplete:     incomplete,
			RawTimestamp:   ts,
			SeqFirst:       group[0].Seq,
			SeqLast:        group[len(group)-1].Seq,
			Segment:        int(timeline.SegmentIndex(ns, spec.OriginNS, spec.SegmentNS, timeline.EdgeOwnsRight)),
		}
		s.Scan = scanSample(spec.Stream, spec.Codec, data, scans)
		s.Keyframe = len(s.Scan.Keyframes) > 0
		s.Damaged = missing > 0 || len(s.Scan.Damaged) > 0
		if s.Damaged && missing > 0 {
			errs = append(errs, merr.New(merr.StageDemux, "sample_missing_packets",
				merr.Span{Stream: spec.Stream, Seq: int64(s.SeqFirst), Offset: -1, Length: int64(missing), TrackID: spec.TrackID}, true,
				"sample %d missing %d packet(s)", s.Index, missing))
		}
		for _, e := range s.Scan.Errors {
			errs = append(errs, e)
		}
		samples = append(samples, s)
		i = j
	}
	// Durations are the forward difference between sample timestamps, exactly
	// in stream ticks; the final sample uses the declared frame duration.
	for k := range samples {
		if k+1 < len(samples) {
			d := samples[k+1].PTS - samples[k].PTS
			if d < 0 {
				d = 0
			}
			samples[k].Dur = d
		} else {
			samples[k].Dur = spec.FrameDuration
		}
		samples[k].DurNS = clock.ToNS(samples[k].Dur)
	}
	return samples, errs
}

func scanSample(stream int, c Codec, data []byte, lim codec.ScanLimits) codec.ScanResult {
	switch c {
	case CodecH264:
		return codec.ScanH264(stream, data, lim)
	case CodecH265:
		return codec.ScanH265(stream, data, lim)
	case CodecAV1:
		return codec.ScanAV1(stream, data, lim)
	default:
		return codec.ScanResult{}
	}
}

func extend(raw uint16, ref int64, primed bool) int64 {
	const wrap = int64(1) << 16
	if !primed {
		return int64(raw)
	}
	base := ref - mod(ref, wrap)
	cand := base + int64(raw)
	if cand-ref > wrap/2 {
		cand -= wrap
	} else if ref-cand > wrap/2 {
		cand += wrap
	}
	return cand
}

func mod(a, b int64) int64 {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}

func totalLen(packets []*transport.Packet) int {
	n := 0
	for _, p := range packets {
		n += len(p.Payload)
	}
	return n
}

// SampleRef identifies a source sample for content comparison.
type SampleRef struct {
	TrackID uint32
	PTS     int64
	Dur     int64
	SHA256  [32]byte
	Size    int
}

// AllSamples flattens streams in stream order.
func (r *Result) AllSamples() []Sample {
	var out []Sample
	for _, st := range r.Streams {
		out = append(out, st.Samples...)
	}
	return out
}

// TrackSamples groups samples by output track, preserving per-track order.
func (r *Result) TrackSamples() map[uint32][]Sample {
	out := make(map[uint32][]Sample, len(r.Streams))
	for _, st := range r.Streams {
		out[st.Spec.TrackID] = append(out[st.Spec.TrackID], st.Samples...)
	}
	for id := range out {
		sort.SliceStable(out[id], func(i, j int) bool {
			if out[id][i].PTS != out[id][j].PTS {
				return out[id][i].PTS < out[id][j].PTS
			}
			return out[id][i].Index < out[id][j].Index
		})
	}
	return out
}

// SampleAt returns the sample with the given track and index.
func (r *Result) SampleAt(track uint32, index int) (Sample, bool) {
	for _, st := range r.Streams {
		if st.Spec.TrackID != track {
			continue
		}
		if index >= 0 && index < len(st.Samples) {
			return st.Samples[index], true
		}
		return Sample{}, false
	}
	return Sample{}, false
}
