package stream

import (
	"encoding/binary"

	"mediacheck/internal/fault"
	"mediacheck/internal/rtp"
)

// location carries every coordinate needed to localize a fault: the
// expanded packet seq span, the half-open byte span in the reassembled
// stream and the unified media-time span.
type location struct {
	seq  fault.SeqRange
	byte fault.ByteRange
	time fault.TimeRange
}

// NALUnit is one reassembled NAL unit plus the coordinates of every
// packet and byte that contributed to it.
type NALUnit struct {
	Type      uint32
	Kind      nalKind
	Data      []byte
	FirstSeq  uint64
	LastSeq   uint64
	ByteOff   int64
	ByteLen   int64
	TimeStart int64
	TimeEnd   int64
	Marker    bool // RTP marker on the final contributing packet
}

// AssembleEvent is everything one packet produced.
type AssembleEvent struct {
	Completed []NALUnit
	Faults    []fault.Fault
}

// Assembler reassembles RTP packets (single NAL, FU-A/FU fragmentation
// and STAP/AP aggregation) into NAL units. A damaged fragment closes
// only the affected unit: the fault is recorded with exact coordinates
// and parsing resumes at the next valid packet.
type Assembler struct {
	codec     Codec
	rec       *fault.Recorder
	bytePos   int64
	frag      *fragment
	streamEnd int64 // expanded time of the latest packet, for fault spans
}

type fragment struct {
	nalType   uint32
	buf       []byte
	firstSeq  uint64
	lastSeq   uint64
	byteStart int64
	timeStart int64
}

// NewAssembler builds an assembler for one codec. Faults are written to
// rec so the caller can merge them with scan-level findings.
func NewAssembler(codec Codec, rec *fault.Recorder) *Assembler {
	return &Assembler{codec: codec, rec: rec}
}

// Push feeds one ordered RTP packet. Returned NAL units are complete;
// partial fragments are retained until their end marker or an error.
func (a *Assembler) Push(op rtp.OrderedPacket) AssembleEvent {
	ev := AssembleEvent{}
	p := op.Packet
	loc := location{
		seq:  fault.SeqRange{First: op.ExpandedSeq, Last: op.ExpandedSeq},
		byte: fault.ByteRange{Offset: a.bytePos, Length: int64(len(p.Payload))},
		time: fault.TimeRange{Start: int64(p.Timestamp), End: int64(p.Timestamp)},
	}
	a.streamEnd = int64(p.Timestamp)

	if len(p.Payload) == 0 {
		a.fail(&ev, loc, fault.ClassIllegalLength, 0, ErrEmptyPayload.Error())
		return ev
	}
	switch {
	case isFragmented(a.codec, p.Payload):
		a.pushFragment(op, loc, &ev)
	case isAggregate(a.codec, p.Payload):
		a.pushAggregate(op, loc, &ev)
	default:
		hdr := parseNALHeader(a.codec, p.Payload[0])
		if hdr.Kind == nalOther {
			a.fail(&ev, loc, fault.ClassUnknownNAL, hdr.Type, "")
		}
		ev.Completed = append(ev.Completed, a.emitUnit(hdr, clone(p.Payload), op, p.Marker))
	}
	return ev
}

// Flush ends the stream. A fragment missing its final FU is reported as
// a truncated NAL with the exact seq/byte/time span observed so far.
func (a *Assembler) Flush() AssembleEvent {
	ev := AssembleEvent{}
	if a.frag == nil {
		return ev
	}
	f := a.frag
	a.frag = nil
	loc := location{
		seq:  fault.SeqRange{First: f.firstSeq, Last: f.lastSeq},
		byte: fault.ByteRange{Offset: f.byteStart, Length: a.bytePos - f.byteStart},
		time: fault.TimeRange{Start: f.timeStart, End: a.streamEnd},
	}
	a.fail(&ev, loc, fault.ClassTruncatedNAL, f.nalType, "fragment ended without final FU")
	return ev
}

func (a *Assembler) pushFragment(op rtp.OrderedPacket, loc location, ev *AssembleEvent) {
	fu, err := parseFU(a.codec, op.Packet.Payload)
	if err != nil {
		a.abandonFragment(ev, loc, fault.ClassIllegalLength, err.Error())
		return
	}
	hdrSize := 2
	if a.codec == CodecH265 {
		hdrSize = 3
	}
	if fu.Start {
		if a.frag != nil {
			// A new fragment starting while another is open means the
			// previous one lost packets: truncate it, then start fresh.
			a.abandonFragment(ev, loc, fault.ClassTruncatedNAL, "new FU start before previous ended")
		}
		indicator := fuIndicatorByte(a.codec, fu.Type, op.Packet.Payload[0])
		a.frag = &fragment{
			nalType:   fu.Type,
			buf:       []byte{indicator},
			firstSeq:  op.ExpandedSeq,
			lastSeq:   op.ExpandedSeq,
			byteStart: a.bytePos,
			timeStart: int64(op.Packet.Timestamp),
		}
	} else {
		if a.frag == nil {
			// Continuation FU without a start: damage localized here.
			a.fail(ev, loc, fault.ClassTruncatedNAL, fu.Type, "FU continuation without start")
			return
		}
		if a.frag.nalType != fu.Type {
			a.abandonFragment(ev, loc, fault.ClassIllegalLength, "FU NAL type changed mid-fragment")
			return
		}
	}
	if len(op.Packet.Payload) <= hdrSize {
		a.abandonFragment(ev, loc, fault.ClassIllegalLength, "FU packet has no fragment payload")
		return
	}
	a.frag.buf = append(a.frag.buf, op.Packet.Payload[hdrSize:]...)
	a.frag.lastSeq = op.ExpandedSeq
	a.bytePos += int64(len(op.Packet.Payload) - hdrSize)

	if fu.End {
		f := a.frag
		a.frag = nil
		hdr := parseNALHeader(a.codec, f.buf[0])
		if hdr.Kind == nalOther {
			loc.seq = fault.SeqRange{First: f.firstSeq, Last: f.lastSeq}
			loc.byte = fault.ByteRange{Offset: f.byteStart, Length: a.bytePos - f.byteStart}
			loc.time = fault.TimeRange{Start: f.timeStart, End: int64(op.Packet.Timestamp)}
			a.fail(ev, loc, fault.ClassUnknownNAL, hdr.Type, "")
		}
		ev.Completed = append(ev.Completed, NALUnit{
			Type:      hdr.Type,
			Kind:      hdr.Kind,
			Data:      f.buf,
			FirstSeq:  f.firstSeq,
			LastSeq:   f.lastSeq,
			ByteOff:   f.byteStart,
			ByteLen:   a.bytePos - f.byteStart,
			TimeStart: f.timeStart,
			TimeEnd:   int64(op.Packet.Timestamp),
			Marker:    op.Packet.Marker,
		})
	}
}

func (a *Assembler) pushAggregate(op rtp.OrderedPacket, loc location, ev *AssembleEvent) {
	headerSize := 1
	if a.codec == CodecH265 {
		headerSize = 2
	}
	body := op.Packet.Payload[headerSize:]
	off := 0
	for off+2 <= len(body) {
		size := int(binary.BigEndian.Uint16(body[off : off+2]))
		off += 2
		if size == 0 || off+size > len(body) {
			loc.byte = fault.ByteRange{Offset: a.bytePos, Length: int64(len(body) - off + 2)}
			a.fail(ev, loc, fault.ClassIllegalLength, 0, "aggregation unit length overruns packet")
			return
		}
		nal := body[off : off+size]
		hdr := parseNALHeader(a.codec, nal[0])
		if hdr.Kind == nalOther {
			loc.byte = fault.ByteRange{Offset: a.bytePos, Length: int64(size)}
			a.fail(ev, loc, fault.ClassUnknownNAL, hdr.Type, "")
		}
		ev.Completed = append(ev.Completed, a.emitUnit(hdr, clone(nal), op, false))
		off += size
	}
	if off != len(body) {
		a.fail(ev, loc, fault.ClassIllegalLength, 0, "trailing bytes in aggregation unit")
	}
}

func (a *Assembler) abandonFragment(ev *AssembleEvent, loc location, class fault.Class, detail string) {
	if a.frag != nil {
		f := a.frag
		a.frag = nil
		loc.seq = fault.SeqRange{First: f.firstSeq, Last: f.lastSeq}
		loc.byte = fault.ByteRange{Offset: f.byteStart, Length: a.bytePos - f.byteStart}
		loc.time = fault.TimeRange{Start: f.timeStart, End: a.streamEnd}
		a.fail(ev, loc, class, f.nalType, detail)
	} else {
		a.fail(ev, loc, class, 0, detail)
	}
}

// emitUnit registers a complete NAL unit and advances the byte cursor.
func (a *Assembler) emitUnit(hdr nalHeader, data []byte, op rtp.OrderedPacket, marker bool) NALUnit {
	start := a.bytePos
	a.bytePos += int64(len(data))
	return NALUnit{
		Type:      hdr.Type,
		Kind:      hdr.Kind,
		Data:      data,
		FirstSeq:  op.ExpandedSeq,
		LastSeq:   op.ExpandedSeq,
		ByteOff:   start,
		ByteLen:   int64(len(data)),
		TimeStart: int64(op.Packet.Timestamp),
		TimeEnd:   int64(op.Packet.Timestamp),
		Marker:    marker,
	}
}

func (a *Assembler) fail(ev *AssembleEvent, loc location, class fault.Class, nalType uint32, detail string) {
	f := fault.Fault{
		Class:   class,
		Codec:   string(a.codec),
		NALType: nalType,
		Seq:     loc.seq,
		Byte:    loc.byte,
		Time:    loc.time,
		Detail:  detail,
	}
	ev.Faults = append(ev.Faults, f)
	a.rec.Add(f)
}

func clone(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
