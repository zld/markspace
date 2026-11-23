// Package rtpparse wraps pion/rtp so the pipeline gets packets with source
// offsets, sequence extension and explicit syntax faults.
//
// The archive hands us RTP datagrams length-prefixed in a small sidecar file.
// Each packet is parsed with pion/rtp and validated for the payload type and
// codec the synchroniser expects. Packet-level faults carry the byte offset of
// the length prefix so a damaged ingest can be traced back to the file.
package rtpparse

import (
	"encoding/binary"
	"io"

	"github.com/pion/rtp"

	"mediarun/internal/fault"
)

// Sidecar constants. A sidecar is:
//
//	magic  [4]byte "RTP1"
//	count  uint32
//	repeat count times:
//	  length uint32
//	  bytes  [length]byte   one RTP datagram
const (
	Magic       = "RTP1"
	MaxDatagram = 1500
	MaxPackets  = 100000
)

// Packet is a parsed RTP datagram with provenance.
type Packet struct {
	Offset     int64  // absolute offset of the length prefix
	Index      int    // zero-based datagram index
	Raw        []byte // exact datagram bytes
	Header     rtp.Header
	Payload    []byte
	ExtSeq     uint64 // extended sequence, resolved by SequenceWindow
	Timestamp  uint32
	SSRC       uint32
	PayloadTyp uint8
	Marker     bool
	Sequence   uint16
}

// ParseSidecar decodes every datagram in a sidecar buffer. Malformed
// datagrams are recorded as faults and skipped; valid packets are returned in
// file order. The returned fault list is never nil when problems occurred.
func ParseSidecar(r io.ReaderAt, size int64) ([]*Packet, fault.List) {
	var faults fault.List
	if size <= 0 {
		faults.Add(fault.New(fault.StageParse, fault.KindIO, "empty rtp sidecar"))
		return nil, faults
	}
	head := make([]byte, 8)
	if size < int64(len(head)) {
		faults.Add(fault.New(fault.StageParse, fault.KindTruncated, "sidecar shorter than header").At(0, -1))
		return nil, faults
	}
	if _, err := r.ReadAt(head, 0); err != nil {
		faults.Add(fault.Wrap(fault.StageParse, fault.KindIO, err, "read sidecar header"))
		return nil, faults
	}
	if string(head[0:4]) != Magic {
		faults.Add(fault.New(fault.StageParse, fault.KindVersion, "bad sidecar magic").At(0, -1))
		return nil, faults
	}
	count := binary.BigEndian.Uint32(head[4:8])
	if count > MaxPackets {
		faults.Add(fault.New(fault.StageParse, fault.KindLimit,
			"sidecar declares %d packets, cap %d", count, MaxPackets).At(0, -1))
		return nil, faults
	}
	packets := make([]*Packet, 0, count)
	pos := int64(8)
	for i := 0; i < int(count); i++ {
		if size-pos < 4 {
			faults.Add(fault.New(fault.StageParse, fault.KindTruncated,
				"packet %d length prefix missing", i).At(pos, i))
			break
		}
		var lenBuf [4]byte
		if _, err := r.ReadAt(lenBuf[:], pos); err != nil {
			faults.Add(fault.Wrap(fault.StageParse, fault.KindIO, err, "read packet %d length", i).At(pos, i))
			break
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		start := pos
		pos += 4
		if n < 12 || n > MaxDatagram {
			faults.Add(fault.New(fault.StageParse, fault.KindLength,
				"packet %d length %d outside 12..%d", i, n, MaxDatagram).At(start, i))
			break
		}
		if size-pos < int64(n) {
			faults.Add(fault.New(fault.StageParse, fault.KindTruncated,
				"packet %d needs %d bytes, %d remain", i, n, size-pos).At(start, i))
			break
		}
		raw := make([]byte, n)
		if _, err := r.ReadAt(raw, pos); err != nil {
			faults.Add(fault.Wrap(fault.StageParse, fault.KindIO, err, "read packet %d", i).At(start, i))
			break
		}
		pkt, err := ParsePacket(raw, start, i)
		if err != nil {
			faults.Add(err)
			pos += int64(n)
			continue
		}
		packets = append(packets, pkt)
		pos += int64(n)
	}
	return packets, faults
}

// ParsePacket parses one raw datagram, annotating faults with its offset.
func ParsePacket(raw []byte, offset int64, index int) (*Packet, error) {
	var h rtp.Header
	if _, err := h.Unmarshal(raw); err != nil {
		e := fault.Wrap(fault.StageParse, fault.KindRTPSyntax, err, "malformed rtp datagram")
		e.At(offset, index)
		return nil, e
	}
	payload := raw[h.MarshalSize():]
	return &Packet{
		Offset:     offset,
		Index:      index,
		Raw:        raw,
		Header:     h,
		Payload:    payload,
		Sequence:   h.SequenceNumber,
		Timestamp:  h.Timestamp,
		SSRC:       h.SSRC,
		PayloadTyp: h.PayloadType,
		Marker:     h.Marker,
	}, nil
}

// ValidateProfile checks payload type and SSRC against the expected profile.
func ValidateProfile(p *Packet, wantPayloadType uint8, wantSSRC uint32) error {
	if p == nil {
		return fault.New(fault.StageParse, fault.KindRTPSyntax, "nil packet")
	}
	if p.PayloadTyp != wantPayloadType {
		e := fault.New(fault.StageParse, fault.KindRTPSyntax,
			"payload type %d, want %d", p.PayloadTyp, wantPayloadType)
		e.At(p.Offset, p.Index)
		e.Recoverable = true
		return e
	}
	if wantSSRC != 0 && p.SSRC != wantSSRC {
		e := fault.New(fault.StageParse, fault.KindRTPSyntax,
			"ssrc %08x, want %08x", p.SSRC, wantSSRC)
		e.At(p.Offset, p.Index)
		e.Recoverable = true
		return e
	}
	return nil
}

// SequenceWindow resolves 16 bit RTP sequence numbers to a monotonic extended
// counter using the RFC 3550 style nearest-value rule. Reordering inside a
// half window keeps packets on their original cycle.
type SequenceWindow struct {
	init    bool
	highest uint64
}

// Extend maps seq to a 64 bit extended sequence number. The first call defines
// the origin.
func (w *SequenceWindow) Extend(seq uint16) uint64 {
	if !w.init {
		w.init = true
		w.highest = uint64(seq)
		return w.highest
	}
	base := w.highest &^ 0xFFFF
	candidate := base + uint64(seq)
	// Choose the cycle nearest to the highest seen value.
	if candidate+0x8000 <= w.highest {
		candidate += 1 << 16
	} else if candidate > w.highest+0x8000 && candidate >= 1<<16 {
		candidate -= 1 << 16
	}
	if candidate > w.highest {
		w.highest = candidate
	}
	return candidate
}

// Highest returns the highest extended sequence observed.
func (w *SequenceWindow) Highest() uint64 { return w.highest }

// Reset clears the window state between fragments.
func (w *SequenceWindow) Reset() { *w = SequenceWindow{} }
