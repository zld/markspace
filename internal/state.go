package rtpreplay

import (
	"fmt"
	"sort"
)

// Duration sources recorded on each packet state so a report can state how
// the media timeline was derived.
const (
	SourceOpusTOC  = "opus_toc"
	SourceAACAU    = "aac_au_config"
	SourceMP4STTS  = "mp4_stts"
	SourceDeclared = "declared"
)

// PacketState is a delivered packet or a declared gap on the reconstructed
// stream. All media values are integer ticks of the stream clock; no float is
// accumulated anywhere in the timeline.
type PacketState struct {
	Track           string  `json:"track"`
	ExtSeq          uint64  `json:"ext_seq"`
	Seq             uint16  `json:"seq"`
	Kind            string  `json:"kind"` // "packet" or "gap"
	ArrivalMs       int64   `json:"arrival_ms"`
	RTPTimestamp    uint32  `json:"rtp_timestamp"`
	MediaStartTicks uint64  `json:"media_start_ticks"`
	MediaEndTicks   uint64  `json:"media_end_ticks"`
	DurationTicks   uint64  `json:"duration_ticks"`
	SizeBytes       int     `json:"size_bytes"`
	DurationSource  string  `json:"duration_source,omitempty"`
	LatencyTicks    int64   `json:"latency_ticks"`
	LatencyMs       float64 `json:"latency_ms"`
	GapCount        uint64  `json:"gap_count,omitempty"`
}

// StreamState is the reconstructed timeline of one stream.
type StreamState struct {
	Track              string
	Codec              Codec
	ClockHz            uint32
	DeclaredDurationMs uint64
	States             []PacketState
	Stats              WindowStats
	Gaps               []GapRun
	MediaTicks         uint64
	BaseArrivalTicks   int64
}

// BuildStreamState reconstructs packet states for one stream. mp4Durations,
// when non-nil, supplies per-sample durations in stream clock ticks from the
// MP4 sample table and is cross-checked against the in-band header.
func BuildStreamState(ps ParsedStream, depth int, mp4Durations []uint64) (*StreamState, error) {
	rb, err := NewReorderBuffer(depth)
	if err != nil {
		return nil, err
	}
	for i := range ps.Packets {
		if err := rb.Push(&ps.Packets[i]); err != nil {
			return nil, err
		}
	}
	emissions, stats := rb.Finish()

	ss := &StreamState{
		Track: ps.Track, Codec: ps.Codec, ClockHz: ps.ClockHz,
		DeclaredDurationMs: ps.DeclaredDurationMs, Stats: stats,
		Gaps: AggregateGaps(emissions),
	}
	var prevRawTS uint32
	var prevUnwrapped int64
	var prevDuration uint64
	var prevExt uint64
	var base int64
	haveTS := false   // timestamp continuity for 32-bit unwrapping
	havePrev := false // adjacency to the previously delivered packet
	deliveredIdx := 0
	for _, e := range emissions {
		if e.Kind == EmitGap {
			ss.States = append(ss.States, PacketState{
				Track: ps.Track, ExtSeq: e.ExtSeq, Kind: "gap", GapCount: e.Count,
				MediaStartTicks: ss.MediaTicks, MediaEndTicks: ss.MediaTicks,
			})
			// A gap breaks sequence adjacency but not the media clock: RTP
			// timestamps keep advancing across the hole, so unwrapping stays
			// continuous while the expected-delta check is suspended.
			havePrev = false
			continue
		}
		p := e.Packet
		dur, source, err := packetDuration(ps, p, deliveredIdx, mp4Durations)
		if err != nil {
			return nil, err
		}
		unwrapped, err := unwrapTimestamp(p.Timestamp, prevRawTS, prevUnwrapped, haveTS)
		if err != nil {
			return nil, AsError(err).WithTrack(ps.Track).WithPacket(p.Seq)
		}
		if havePrev && e.ExtSeq == prevExt+1 && unwrapped-prevUnwrapped != int64(prevDuration) {
			return nil, NewError(CodeTimestampJump, "reconstruct",
				fmt.Sprintf("RTP timestamp advanced %d ticks, expected %d from AU duration", unwrapped-prevUnwrapped, prevDuration)).
				WithTrack(ps.Track).WithPacket(p.Seq)
		}
		if deliveredIdx == 0 {
			// Timeline origin: the first delivered packet defines both the
			// media base and the arrival base used for latency.
			base = unwrapped
			ss.BaseArrivalTicks = arrivalTicks(p.ArrivalMs, ps.ClockHz)
		}
		start := unwrapped - base
		if start < 0 {
			return nil, NewError(CodeTimestampJump, "reconstruct", "negative media position after unwrap").
				WithTrack(ps.Track).WithPacket(p.Seq)
		}
		latTicks := arrivalTicks(p.ArrivalMs, ps.ClockHz) - ss.BaseArrivalTicks - start
		if start+int64(dur) > int64(ss.MediaTicks) {
			ss.MediaTicks = uint64(start + int64(dur))
		}
		ss.States = append(ss.States, PacketState{
			Track: ps.Track, ExtSeq: e.ExtSeq, Seq: p.Seq, Kind: "packet",
			ArrivalMs: p.ArrivalMs, RTPTimestamp: p.Timestamp,
			MediaStartTicks: uint64(start), MediaEndTicks: uint64(start + int64(dur)),
			DurationTicks: dur, SizeBytes: len(p.Payload), DurationSource: source,
			LatencyTicks: latTicks,
			LatencyMs:    float64(latTicks) * 1000 / float64(ps.ClockHz),
		})
		prevRawTS, prevUnwrapped, prevDuration, prevExt = p.Timestamp, unwrapped, dur, e.ExtSeq
		haveTS = true
		havePrev = true
		deliveredIdx++
	}
	if deliveredIdx == 0 {
		return nil, NewError(CodeNoData, "reconstruct", "stream produced no delivered packets").WithTrack(ps.Track)
	}
	return ss, nil
}

// packetDuration resolves one packet's duration in stream clock ticks.
func packetDuration(ps ParsedStream, p *Packet, idx int, mp4 []uint64) (uint64, string, error) {
	switch ps.Codec {
	case CodecOpus:
		toc, err := ParseOpusTOC(p.Payload)
		if err != nil {
			return 0, "", AsError(err).WithTrack(ps.Track).WithPacket(p.Seq)
		}
		d, err := toc.DurationTicks(ps.ClockHz)
		if err != nil {
			return 0, "", AsError(err).WithTrack(ps.Track).WithPacket(p.Seq)
		}
		return d, SourceOpusTOC, nil
	case CodecAAC:
		var auDur uint64
		source := SourceAACAU
		if ps.AAC != nil && ps.AAC.UseAUHeaders {
			if err := validateAUHeader(p.Payload, *ps.AAC); err != nil {
				return 0, "", AsError(err).WithTrack(ps.Track).WithPacket(p.Seq)
			}
		}
		if ps.AAC == nil {
			return 0, "", NewError(CodeDuration, "aac_duration", "AAC stream has no configuration").
				WithTrack(ps.Track).WithPacket(p.Seq)
		}
		// AU duration is constant for AAC: 1024 or 960 samples per AU.
		rate := ps.AAC.SampleRate
		if rate == 0 {
			return 0, "", NewError(CodeDuration, "aac_duration", "AAC sample rate unknown").
				WithTrack(ps.Track).WithPacket(p.Seq)
		}
		d, err := Scale(uint64(ps.AAC.SamplesPerFrame()), rate, ps.ClockHz)
		if err != nil {
			return 0, "", AsError(err).WithTrack(ps.Track).WithPacket(p.Seq)
		}
		auDur = d
		if mp4 != nil {
			if idx >= len(mp4) {
				return 0, "", NewError(CodeDuration, "aac_duration", "MP4 sample table shorter than stream").
					WithTrack(ps.Track).WithPacket(p.Seq)
			}
			if mp4[idx] != auDur {
				return 0, "", NewError(CodeDuration, "aac_duration",
					fmt.Sprintf("MP4 sample duration %d ticks disagrees with AU duration %d", mp4[idx], auDur)).
					WithTrack(ps.Track).WithPacket(p.Seq)
			}
			return mp4[idx], SourceMP4STTS, nil
		}
		return auDur, source, nil
	default:
		return 0, "", NewError(CodeUnknownCodec, "duration", string(ps.Codec)).WithTrack(ps.Track).WithPacket(p.Seq)
	}
}

// validateAUHeader parses the RFC 3640 AU-headers-length prefix and checks
// that the declared AU sizes fit inside the payload.
func validateAUHeader(payload []byte, cfg AACStreamConfig) error {
	if len(payload) < 2 {
		return NewError(CodeAACHeader, "aac_au_header", "payload too short for AU-headers-length")
	}
	bitsLen := int(payload[0])<<8 | int(payload[1])
	if bitsLen == 0 {
		return NewError(CodeAACHeader, "aac_au_header", "AU-headers-length is zero")
	}
	if bitsLen%8 != 0 {
		return NewError(CodeAACHeader, "aac_au_header", "AU header length is not byte aligned")
	}
	headerBytes := bitsLen / 8
	if len(payload) < 2+headerBytes {
		return NewError(CodeAACHeader, "aac_au_header", "AU header truncated")
	}
	fields := int(cfg.SizeLength) + int(cfg.IndexLength)
	if fields == 0 || bitsLen%fields != 0 {
		return NewError(CodeAACHeader, "aac_au_header", "AU header does not decompose into size/index fields")
	}
	count := bitsLen / fields
	br := bitReader{data: payload[2 : 2+headerBytes]}
	total := 0
	for i := 0; i < count; i++ {
		size, ok := br.read(uint(cfg.SizeLength))
		if !ok {
			return NewError(CodeAACHeader, "aac_au_header", "truncated AU-size field")
		}
		if _, ok := br.read(uint(cfg.IndexLength)); !ok {
			return NewError(CodeAACHeader, "aac_au_header", "truncated AU-index field")
		}
		total += int(size)
	}
	if total > len(payload)-2-headerBytes {
		return NewError(CodeAACHeader, "aac_au_header",
			fmt.Sprintf("AU sizes total %d bytes but only %d available", total, len(payload)-2-headerBytes))
	}
	return nil
}

func arrivalTicks(ms int64, rate uint32) int64 {
	if ms <= 0 {
		return 0
	}
	return int64(uint64(ms) * uint64(rate) / 1000)
}

func unwrapTimestamp(ts, prevRaw uint32, prevUnwrapped int64, havePrev bool) (int64, error) {
	if !havePrev {
		return int64(ts), nil
	}
	delta := int64(int32(ts - prevRaw))
	v := prevUnwrapped + delta
	if v < 0 {
		return 0, NewError(CodeTimestampJump, "unwrap", "timestamp unwrap produced negative value")
	}
	return v, nil
}

// SortStatesByMedia orders states by media start then extended sequence.
func SortStatesByMedia(states []PacketState) {
	sort.SliceStable(states, func(i, j int) bool {
		if states[i].MediaStartTicks != states[j].MediaStartTicks {
			return states[i].MediaStartTicks < states[j].MediaStartTicks
		}
		return states[i].ExtSeq < states[j].ExtSeq
	})
}
