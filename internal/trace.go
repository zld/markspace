package rtpreplay

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/pion/rtp"
)

// Trace is the offline event log: an ordered list of raw RTP datagrams,
// bandwidth change events, and a reference to the generated MP4 sample table.
// It is the sole input to the three subsystems and is reproducible from the
// generator seed.
type Trace struct {
	Version string       `json:"version"`
	Seed    int64        `json:"seed"`
	Clock   TraceClock   `json:"clock"`
	Streams []Stream     `json:"streams"`
	Events  []TimedEvent `json:"events"`
	MP4     *MP4Ref      `json:"mp4,omitempty"`
}

// TraceClock records the clock rates used by the trace.
type TraceClock struct {
	RTPRateHz   uint32 `json:"rtp_rate_hz"`
	EventRateHz uint32 `json:"event_rate_hz"`
}

// Stream is one media track carried by the trace.
type Stream struct {
	Track              string           `json:"track"`
	Codec              Codec            `json:"codec"`
	ClockHz            uint32           `json:"clock_hz"`
	DeclaredDurationMs uint64           `json:"declared_duration_ms"`
	AAC                *AACStreamConfig `json:"aac,omitempty"`
	Packets            []TracePacket    `json:"packets"`
}

// AACStreamConfig describes the RFC 3640 mpeg4-generic payload framing.
type AACStreamConfig struct {
	UseAUHeaders bool  `json:"use_au_headers"`
	SizeLength   uint8 `json:"size_length"`
	IndexLength  uint8 `json:"index_length"`
}

// TracePacket is one RTP arrival. RTPHex is the complete datagram so that the
// parser exercises real RTP framing; arrival time lives outside the datagram.
type TracePacket struct {
	RTPHex    string `json:"rtp_hex"`
	ArrivalMs int64  `json:"arrival_ms"`
	Fault     string `json:"fault,omitempty"`
}

// TimedEvent is a non-media input event on the trace clock.
type TimedEvent struct {
	AtMs     int64  `json:"at_ms"`
	Kind     string `json:"kind"`
	Track    string `json:"track,omitempty"`
	ValueBps int64  `json:"value_bps,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Event kinds.
const EventBandwidth = "bandwidth"

// MP4Ref points at the generated MP4 sample table by logical name and content
// digest. The bytes themselves are generated alongside the trace.
type MP4Ref struct {
	FileName string `json:"file_name"`
	Sha256   string `json:"sha256"`
}

// ParsedTrace is a validated trace with RTP datagrams decoded by pion/rtp.
type ParsedTrace struct {
	Raw         *Trace
	InputDigest string
	Streams     []ParsedStream
	Events      []TimedEvent
	MP4         *MP4Ref
}

// ParsedStream holds decoded arrivals for one track.
type ParsedStream struct {
	Track              string
	Codec              Codec
	ClockHz            uint32
	DeclaredDurationMs uint64
	AAC                *AACStreamConfig
	Packets            []Packet
}

// ParseTrace decodes a JSON trace, parses every RTP datagram, and validates
// structural invariants. Malformed content yields a structured error.
func ParseTrace(data []byte) (*ParsedTrace, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var tr Trace
	if err := dec.Decode(&tr); err != nil {
		return nil, NewError(CodeInvalidInput, "parse_trace", err.Error()).WithCause(err)
	}
	if err := tr.validate(); err != nil {
		return nil, err
	}
	out := &ParsedTrace{Raw: &tr, InputDigest: tr.Digest(), MP4: tr.MP4}
	for i := range tr.Streams {
		s := &tr.Streams[i]
		ps := ParsedStream{
			Track: s.Track, Codec: s.Codec, ClockHz: s.ClockHz,
			DeclaredDurationMs: s.DeclaredDurationMs, AAC: s.AAC,
			Packets: make([]Packet, 0, len(s.Packets)),
		}
		for j := range s.Packets {
			p, err := decodeRTPPacket(s.Track, &s.Packets[j])
			if err != nil {
				return nil, err
			}
			ps.Packets = append(ps.Packets, p)
		}
		out.Streams = append(out.Streams, ps)
	}
	out.Events = append([]TimedEvent(nil), tr.Events...)
	sort.SliceStable(out.Events, func(i, j int) bool { return out.Events[i].AtMs < out.Events[j].AtMs })
	return out, nil
}

func decodeRTPPacket(track string, tp *TracePacket) (Packet, error) {
	raw, err := hex.DecodeString(tp.RTPHex)
	if err != nil {
		return Packet{}, NewError(CodeRTPParse, "decode_rtp", "rtp_hex is not valid hex").WithTrack(track).WithCause(err)
	}
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return Packet{}, NewError(CodeRTPParse, "decode_rtp", err.Error()).WithTrack(track).WithCause(err)
	}
	return Packet{
		Track:     track,
		Seq:       pkt.SequenceNumber,
		Timestamp: pkt.Timestamp,
		Marker:    pkt.Marker,
		Payload:   pkt.Payload,
		ArrivalMs: tp.ArrivalMs,
		Fault:     tp.Fault,
	}, nil
}

func (tr *Trace) validate() error {
	if len(tr.Streams) == 0 {
		return NewError(CodeNoData, "parse_trace", "trace has no streams")
	}
	if tr.Clock.RTPRateHz == 0 || tr.Clock.EventRateHz == 0 {
		return NewError(CodeInvalidConfig, "parse_trace", "trace clock rates must be positive")
	}
	for i := range tr.Streams {
		s := &tr.Streams[i]
		if s.Track == "" {
			return NewError(CodeInvalidInput, "parse_trace", fmt.Sprintf("stream %d has no track", i))
		}
		if s.Codec != CodecOpus && s.Codec != CodecAAC {
			return NewError(CodeUnknownCodec, "parse_trace", fmt.Sprintf("codec %q unsupported", s.Codec)).WithTrack(s.Track)
		}
		if s.ClockHz == 0 {
			return NewError(CodeInvalidConfig, "parse_trace", "stream clock must be positive").WithTrack(s.Track)
		}
		if len(s.Packets) == 0 {
			return NewError(CodeNoData, "parse_trace", "stream has no packets").WithTrack(s.Track)
		}
		if s.Codec == CodecAAC && s.AAC != nil && s.AAC.UseAUHeaders {
			if s.AAC.SizeLength == 0 || s.AAC.SizeLength > 16 || s.AAC.IndexLength > 16 {
				return NewError(CodeInvalidConfig, "parse_trace", "invalid AAC AU header field widths").WithTrack(s.Track)
			}
		}
		for j := range s.Packets {
			if s.Packets[j].ArrivalMs < 0 {
				return NewError(CodeInvalidInput, "parse_trace", "arrival_ms must be non-negative").WithTrack(s.Track)
			}
		}
	}
	for _, ev := range tr.Events {
		switch ev.Kind {
		case EventBandwidth:
			if ev.ValueBps <= 0 {
				return NewError(CodeInvalidInput, "parse_trace", "bandwidth event must carry positive value_bps")
			}
		default:
			return NewError(CodeInvalidInput, "parse_trace", fmt.Sprintf("unknown event kind %q", ev.Kind))
		}
	}
	if tr.MP4 != nil && tr.MP4.Sha256 == "" {
		return NewError(CodeInvalidInput, "parse_trace", "mp4 reference missing sha256")
	}
	return nil
}

// Digest returns a stable SHA-256 over the canonical JSON form of the trace.
// Events are sorted so arrival ordering of equal-time events cannot perturb it.
func (tr *Trace) Digest() string {
	cp := *tr
	cp.Events = append([]TimedEvent(nil), tr.Events...)
	sort.SliceStable(cp.Events, func(i, j int) bool {
		if cp.Events[i].AtMs != cp.Events[j].AtMs {
			return cp.Events[i].AtMs < cp.Events[j].AtMs
		}
		return cp.Events[i].Kind < cp.Events[j].Kind
	})
	b, _ := json.Marshal(cp)
	return sha256Hex(b)
}
