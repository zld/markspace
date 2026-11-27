// Package ts: PES depacketisation.
//
// Elementary audio is reassembled from trusted transport packets only.
// Discontinuities are converted into explicit gaps on the source sample
// clock; this layer never inserts, drops or reorders audio to hide damage.
package ts

import (
	"example.com/assetpack/internal/fault"
)

// PES start codes.
const (
	PESStreamMPEG2Audio = 0xC0
	PESStreamPrivate1   = 0xBD
	PESStreamPadding    = 0xBE
)

// ESChunk is a contiguous run of elementary audio bytes.
type ESChunk struct {
	SourceOffset int64
	PTS          int64 // 90 kHz ticks; -1 when absent
	Data         []byte
}

// ElementGap is missing source material on the source sample clock.
type ElementGap struct {
	StartSample  int64 `json:"start_sample"`
	LengthSample int64 `json:"length_samples"`
	SourceOffset int64 `json:"source_offset"`
}

// AudioElement is the audio elementary stream recovered from a transport
// stream, together with the gaps discovered while recovering it.
type AudioElement struct {
	PID       uint16
	Chunks    []ESChunk
	Gaps      []ElementGap
	Packets   int
	LostBytes int64
	Faults    *fault.List
}

// Bytes is the total elementary payload length.
func (e *AudioElement) Bytes() int {
	n := 0
	for _, c := range e.Chunks {
		n += len(c.Data)
	}
	return n
}

// DemuxAudio extracts the payload of the given audio PID.  The frame size in
// bytes (channels * bytes per sample) converts lost bytes into a sample gap.
func DemuxAudio(s *Stream, pid uint16, frameBytes int) *AudioElement {
	el := &AudioElement{PID: pid, Faults: &fault.List{}}
	if s == nil {
		el.Faults.Add(fault.New(fault.StageParse, fault.KindMissing, "no transport stream"))
		return el
	}
	if frameBytes <= 0 {
		frameBytes = 1
	}
	var pending []byte
	var pendingOff int64
	var pendingPTS int64 = -1
	flush := func() {
		if len(pending) == 0 {
			return
		}
		el.Chunks = append(el.Chunks, ESChunk{
			SourceOffset: pendingOff,
			PTS:          pendingPTS,
			Data:         append([]byte(nil), pending...),
		})
		pending = nil
		pendingPTS = -1
	}
	for i := range s.Packets {
		pkt := &s.Packets[i]
		if pkt.PID != pid || pkt.Payload == nil {
			continue
		}
		el.Packets++
		chunk, ok := parsePES(pkt.Payload, pkt.PUSI)
		if !ok {
			continue
		}
		if chunk.reset {
			flush()
			pendingOff = pkt.Offset
			pendingPTS = chunk.pts
		} else if len(pending) == 0 {
			pendingOff = pkt.Offset
			pendingPTS = chunk.pts
		}
		pending = append(pending, chunk.data...)
	}
	flush()

	// Continuity faults on the audio PID mean material was withheld; turn
	// each into a gap measured in source frames.
	for _, f := range s.Faults.Items {
		if f.Kind != fault.KindCCJump && f.Kind != fault.KindCCDuplicate && f.Kind != fault.KindTruncated {
			continue
		}
		el.Gaps = append(el.Gaps, ElementGap{SourceOffset: f.Offset})
	}
	el.Faults.Merge(s.Faults)
	return el
}

type pesChunk struct {
	reset bool // PES header started here
	pts   int64
	data  []byte
}

// parsePES reads one PES packet fragment.  A start code may only introduce a
// PES packet when the transport packet's payload_unit_start_indicator is set;
// byte sequences that merely look like a start code inside audio payload are
// never treated as headers.
func parsePES(payload []byte, pusi bool) (pesChunk, bool) {
	if !pusi {
		return pesChunk{data: payload}, true
	}
	if len(payload) < 9 {
		return pesChunk{}, false
	}
	if payload[0] != 0 || payload[1] != 0 || payload[2] != 1 {
		return pesChunk{}, false
	}
	streamID := payload[3]
	if streamID == PESStreamPadding {
		return pesChunk{}, false
	}
	headerLen := int(payload[8])
	if 9+headerLen > len(payload) {
		return pesChunk{}, false
	}
	pts := int64(-1)
	flags := payload[7]
	if flags&0x80 != 0 && headerLen >= 5 {
		p := payload[9:14]
		pts = int64(p[0]>>1&0x07)<<30 |
			int64(p[1])<<22 | int64(p[2]>>1)<<15 |
			int64(p[3])<<7 | int64(p[4]>>1)
	}
	return pesChunk{reset: true, pts: pts, data: payload[9+headerLen:]}, true
}

// FirstPTS returns the earliest PTS in the element, or -1.
func (e *AudioElement) FirstPTS() int64 {
	for _, c := range e.Chunks {
		if c.PTS >= 0 {
			return c.PTS
		}
	}
	return -1
}

// SourceGaps resolves missing material onto the source sample clock.  Each
// chunk that carries a PTS has a position implied by the first PTS; when that
// implied position is ahead of the frames recovered so far, the difference is
// a gap.  Missing material is never inferred from packet sizes alone.
func (e *AudioElement) SourceGaps(rate int, frameBytes int) []ElementGap {
	if rate <= 0 || frameBytes <= 0 {
		return nil
	}
	var gaps []ElementGap
	var frames int64
	var firstPTS int64 = -1
	for _, c := range e.Chunks {
		if c.PTS >= 0 {
			if firstPTS < 0 {
				firstPTS = c.PTS
			}
			implied := (c.PTS - firstPTS) * int64(rate) / int64(PCRClockHz)
			if implied > frames {
				gaps = append(gaps, ElementGap{
					StartSample:  frames,
					LengthSample: implied - frames,
					SourceOffset: c.SourceOffset,
				})
				frames = implied
			}
		}
		frames += int64(len(c.Data) / frameBytes)
	}
	for _, g := range e.Gaps {
		if g.LengthSample > 0 {
			gaps = append(gaps, g)
		}
	}
	return gaps
}

// SampleCount is the recovered frame count from payload bytes.
func (e *AudioElement) SampleCount(frameBytes int) int64 {
	if frameBytes <= 0 {
		return 0
	}
	return int64(e.Bytes() / frameBytes)
}

// DecodePCM16LE splits interleaved little-endian PCM into the deinterleaved
// representation used by the audio package.  Sample values are scaled to the
// -1..1 range.  Partial trailing frames are reported as a recoverable
// truncation but never silently dropped from the frame count.
func (e *AudioElement) DecodePCM16LE(channels int) ([]byte, error) {
	if channels <= 0 {
		return nil, fault.New(fault.StageAudio, fault.KindInvalidArg, "invalid channel count %d", channels)
	}
	frameBytes := channels * 2
	out := make([]byte, 0, e.Bytes()-e.Bytes()%frameBytes)
	for _, c := range e.Chunks {
		out = append(out, c.Data...)
	}
	usable := len(out) - len(out)%frameBytes
	if usable != len(out) {
		e.Faults.Add(fault.New(fault.StageParse, fault.KindTruncated,
			"elementary stream ends with %d bytes that are not a full %d-byte frame",
			len(out)-usable, frameBytes).Recover(true))
	}
	return out[:usable], nil
}

// PTSBytes encodes a 33-bit PTS in PES header format; exported for the
// fixture generator and for round-trip tests.
func PTSBytes(pts int64) []byte {
	return []byte{
		byte(0x21 | byte(pts>>29)&0x0E),
		byte(pts >> 22),
		byte(0x01 | byte(pts>>14)&0xFE),
		byte(pts >> 7),
		byte(0x01 | byte(pts<<1)&0xFE),
	}
}
