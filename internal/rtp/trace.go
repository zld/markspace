// Package rtp parses offline RTP traces, reorders them by sequence number and
// accounts for loss, duplication and late arrivals.
package rtp

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"

	prtp "github.com/pion/rtp"

	"rtpregress/internal/media"
)

// Header is the trace-level metadata. Every packet in one trace shares the
// same track, clock and codec.
type Header struct {
	// Track is the logical track name, e.g. "audio", "video", "meta".
	Track string
	// Timescale is the RTP clock rate in ticks per second.
	Timescale uint32
	// Codec names the payload layout used to recover timed metadata:
	// "opus", "h264", "id3" or "raw".
	Codec string
	// PayloadType is the RTP payload type used by all packets in the trace.
	PayloadType uint8
}

// Packet is one RTP packet plus the arrival ordering information that only
// exists in the trace file, not in the RTP header.
type Packet struct {
	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
	PayloadType    uint8
	Marker         bool
	Payload        []byte
	// ArrivalNs is the trace author's arrival time on the common timeline.
	ArrivalNs int64
	// Offset is the byte offset of the packet's trace line in the file.
	Offset int64
	// Line is the 1-based trace line number, used in error messages.
	Line int
}

// Trace is a fully parsed RTP trace.
type Trace struct {
	Header  Header
	Packets []Packet
	// Size is the number of bytes consumed from the trace file.
	Size int64
}

// Timebase returns the track clock with the first packet's timestamp as
// origin, so packet timestamps map onto media time without a separate
// reference clock.
func (t *Trace) Timebase() media.Timebase {
	var origin int64
	if len(t.Packets) > 0 {
		origin = int64(t.Packets[0].Timestamp)
	}
	return media.Timebase{
		Track:       t.Header.Track,
		Timescale:   t.Header.Timescale,
		OriginTicks: origin,
		OriginNs:    0,
	}
}

// TimestampNs maps a 32-bit RTP timestamp onto the common nanosecond timeline.
// Timestamp wrap is handled modulo 2^32 relative to the first packet; a trace
// spanning a full wrap is rejected as ambiguous.
func (t *Trace) TimestampNs(ts uint32) (int64, error) {
	if len(t.Packets) == 0 {
		return 0, media.Newf(media.KindInput, "trace %q has no packets", t.Header.Track)
	}
	// The 32-bit signed difference resolves wraparound relative to the
	// first packet; adding it to that packet's absolute tick yields a
	// monotonic tick value even across the 2^32 boundary.
	delta := int64(int32(ts - t.Packets[0].Timestamp))
	abs := int64(t.Packets[0].Timestamp) + delta
	if abs < 0 {
		abs += 1 << 32
	}
	ns, err := t.Timebase().ToNs(uint64(abs))
	if err != nil {
		return 0, media.At(err, media.Loc{Track: t.Header.Track, Stage: "parse"})
	}
	return ns, nil
}

// ParseTrace reads the textual trace format:
//
//	track audio
//	timescale 48000
//	codec opus
//	payload 111
//	rtp <lowercase hex of the full RTP packet> <arrival nanoseconds>
//
// Blank lines and lines beginning with '#' are ignored. Every packet line is
// decoded with pion/rtp, so malformed RTP headers fail with the line and byte
// offset rather than being skipped.
func ParseTrace(r io.Reader) (*Trace, error) {
	br := bufio.NewReader(r)
	tr := &Trace{}
	var (
		offset   int64
		lineNo   int
		haveHead bool
	)
	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, media.At(media.Newf(media.KindInput, "read trace: %v", err),
				media.Loc{Offset: offset, HasOffset: true, Stage: "parse"})
		}
		if line == "" && err == io.EOF {
			break
		}
		start := offset
		offset += int64(len(line))
		lineNo++
		text := strings.TrimSpace(line)
		switch {
		case text == "" || strings.HasPrefix(text, "#"):
			// skip
		case strings.HasPrefix(text, "track "):
			tr.Header.Track = strings.TrimSpace(text[len("track "):])
		case strings.HasPrefix(text, "timescale "):
			v := strings.TrimSpace(text[len("timescale "):])
			n, perr := strconv.ParseUint(v, 10, 32)
			if perr != nil {
				return nil, traceErr(tr.Header.Track, lineNo, start, "bad timescale %q", v)
			}
			tr.Header.Timescale = uint32(n)
		case strings.HasPrefix(text, "codec "):
			tr.Header.Codec = strings.TrimSpace(text[len("codec "):])
		case strings.HasPrefix(text, "payload "):
			v := strings.TrimSpace(text[len("payload "):])
			n, perr := strconv.ParseUint(v, 10, 8)
			if perr != nil {
				return nil, traceErr(tr.Header.Track, lineNo, start, "bad payload type %q", v)
			}
			tr.Header.PayloadType = uint8(n)
		case strings.HasPrefix(text, "rtp "):
			haveHead = true
			pkt, perr := parsePacketLine(tr.Header, text[len("rtp "):], start, lineNo)
			if perr != nil {
				return nil, perr
			}
			tr.Packets = append(tr.Packets, pkt)
		default:
			return nil, traceErr(tr.Header.Track, lineNo, start, "unknown directive %q", firstWord(text))
		}
		if err == io.EOF {
			break
		}
	}
	if tr.Header.Track == "" {
		return nil, media.At(media.New(media.KindInput, "trace is missing the track directive"),
			media.Loc{Stage: "parse"})
	}
	if err := (media.Timebase{Track: tr.Header.Track, Timescale: tr.Header.Timescale}).Validate(); err != nil {
		return nil, media.At(err, media.Loc{Stage: "parse"})
	}
	if tr.Header.Codec == "" {
		return nil, traceErr(tr.Header.Track, 1, 0, "trace header is missing the codec directive")
	}
	if !haveHead || len(tr.Packets) == 0 {
		return nil, traceErr(tr.Header.Track, 1, 0, "trace contains no RTP packets")
	}
	tr.Size = offset
	return tr, nil
}

func parsePacketLine(h Header, body string, offset int64, line int) (Packet, error) {
	fields := strings.Fields(body)
	if len(fields) != 2 {
		return Packet{}, traceErr(h.Track, line, offset, "packet line needs hex and arrival time, got %d fields", len(fields))
	}
	raw, err := hex.DecodeString(fields[0])
	if err != nil {
		return Packet{}, traceErr(h.Track, line, offset, "invalid packet hex: %v", err)
	}
	if len(raw) < 12 {
		return Packet{}, traceErr(h.Track, line, offset, "RTP packet is %d bytes, shorter than the 12-byte header", len(raw))
	}
	var p prtp.Packet
	if err := p.Unmarshal(raw); err != nil {
		return Packet{}, traceErr(h.Track, line, offset, "invalid RTP packet: %v", err)
	}
	if p.Version != 2 {
		return Packet{}, traceErr(h.Track, line, offset, "unsupported RTP version %d", p.Version)
	}
	arrival, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return Packet{}, traceErr(h.Track, line, offset, "invalid arrival time %q", fields[1])
	}
	if arrival < 0 {
		return Packet{}, traceErr(h.Track, line, offset, "negative arrival time %d", arrival)
	}
	return Packet{
		SequenceNumber: p.SequenceNumber,
		Timestamp:      p.Timestamp,
		SSRC:           p.SSRC,
		PayloadType:    p.PayloadType,
		Marker:         p.Marker,
		Payload:        p.Payload,
		ArrivalNs:      arrival,
		Offset:         offset,
		Line:           line,
	}, nil
}

func traceErr(track string, line int, offset int64, format string, args ...any) error {
	return media.At(media.Newf(media.KindInput, format, args...), media.Loc{
		Track: track, Sample: line, Stage: "parse", Offset: offset, HasOffset: true,
	})
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

// EncodePacketLine is the inverse of the packet-line parser. Fixture
// generation and tests use it to keep the trace format in one place.
func EncodePacketLine(raw []byte, arrivalNs int64) string {
	return "rtp " + hex.EncodeToString(raw) + " " + strconv.FormatInt(arrivalNs, 10) + "\n"
}

// MarshalPacket serialises an RTP packet for trace encoding. pion/rtp
// requires the destination to be large enough up front; MarshalSize gives
// the exact length.
func MarshalPacket(p *prtp.Packet) ([]byte, error) {
	buf := make([]byte, p.MarshalSize())
	n, err := p.MarshalTo(buf)
	if err != nil {
		return nil, fmt.Errorf("marshal RTP packet: %w", err)
	}
	return buf[:n], nil
}

// HasPayload reports whether a packet carries a non-empty payload.
func (p Packet) HasPayload() bool {
	return len(bytes.TrimSpace(p.Payload)) > 0
}
