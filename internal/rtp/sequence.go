// Package rtp turns RTP packet headers into a lossless, wrap-aware sequence
// space on the shared media time base. Payload bytes are opaque: the kernel
// orders, deduplicates and classifies packets but never interprets codec data.
package rtp

import (
	"encoding/binary"
	"fmt"
	"sort"

	pion "github.com/pion/rtp"

	"mediasync/internal/model"
)

// SeqBits is the number of significant bits in an RTP sequence number.
const SeqBits = 16

// Header is the subset of an RTP header the kernel keeps.
type Header struct {
	TrackID    uint32
	SSRC       uint32
	PayloadTyp uint8
	Marker     bool
	Seq        uint16
	Timestamp  uint32
	// Arrival is the local wall-clock arrival index supplied by the capture
	// log. It is monotonically increasing per track and breaks ties
	// deterministically without consulting the system clock.
	Arrival int64
	// Size is the payload length in bytes.
	Size int
}

// Parse decodes one serialized RTP packet into a Header.
func Parse(trackID uint32, arrival int64, raw []byte, file string, offset int64) (Header, error) {
	loc := model.Loc{File: file, TrackID: trackID, HasByteOffset: true, ByteOffset: offset}
	if len(raw) < 12 {
		return Header{}, model.NewError(model.CodeRTPHeader, loc, "rtp packet is %d bytes, need at least 12", len(raw))
	}
	var p pion.Packet
	if err := p.Unmarshal(raw); err != nil {
		return Header{}, model.NewError(model.CodeRTPHeader, loc, "invalid RTP header").Wrap(err)
	}
	return Header{
		TrackID:    trackID,
		SSRC:       p.SSRC,
		PayloadTyp: p.PayloadType,
		Marker:     p.Marker,
		Seq:        p.SequenceNumber,
		Timestamp:  p.Timestamp,
		Arrival:    arrival,
		Size:       len(p.Payload),
	}, nil
}

// Extender unfolds a wrapped unsigned value into a monotonically comparable
// extended space. It implements the standard RFC 3711/3550 style "closest
// extension" rule: a wrapped value is placed in the same cycle as the
// reference unless it is nearer to the adjacent cycle.
type Extender struct {
	bits      uint
	modulus   uint64
	halfRange uint64
	// high is the current cycle base (the extended value minus its low bits).
	high     int64
	highSet  bool
	lastSeen int64
}

// NewExtender builds an extender for values of the given bit width (1..63).
func NewExtender(bits uint) (*Extender, error) {
	if bits == 0 || bits > 63 {
		return nil, fmt.Errorf("wrap width must be in 1..63, got %d", bits)
	}
	return &Extender{bits: bits, modulus: uint64(1) << bits, halfRange: uint64(1) << (bits - 1)}, nil
}

// Bits returns the configured bit width.
func (e *Extender) Bits() uint { return e.bits }

// Reset forgets the current cycle base.
func (e *Extender) Reset() {
	e.high = 0
	e.highSet = false
	e.lastSeen = 0
}

// Extend maps a raw value into the extended space relative to the current
// reference. The first value establishes the base at its own magnitude.
//
// It reports whether the value was smaller than the previous extended value
// (an out-of-order arrival).
func (e *Extender) Extend(raw uint64) (ext int64, backward bool) {
	raw &= e.modulus - 1
	if !e.highSet {
		e.high = int64(raw)
		e.highSet = true
		e.lastSeen = int64(raw)
		return e.high, false
	}
	ext = e.high + int64(raw)
	// Consider the neighbouring cycles and pick the closest one.
	for _, delta := range []int64{-int64(e.modulus), 0, int64(e.modulus)} {
		cand := e.high + delta + int64(raw)
		if abs64(cand-e.lastSeen) < abs64(ext-e.lastSeen) {
			ext = cand
		}
	}
	backward = ext < e.lastSeen
	e.lastSeen = ext
	return ext, backward
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// ParseBits validates and extracts the wrap width of a raw value, which is
// used by tests to ensure the mask is applied before comparison.
func maskValue(v uint64, bits uint) uint64 {
	if bits >= 64 {
		return v
	}
	return v & ((uint64(1) << bits) - 1)
}

// Sequence is one packet in the extended sequence space.
type Sequence struct {
	Ext       int64
	Header    Header
	Duplicate bool
	Late      bool
	// LateBy is how far the packet's extended sequence trails the highest
	// extended sequence observed when it arrived.
	LateBy int64
}

// Deduper assigns extended sequence numbers to headers and marks duplicates.
// It is not safe for concurrent use.
type Deduper struct {
	ext      *Extender
	seen     map[int64]struct{}
	highExt  int64
	highInit bool
	// window is the maximum backward distance, in packets, that is still
	// buffered rather than flagged as late.
	window int64
}

// NewDeduper builds a deduplicator over the given bit width and late window.
func NewDeduper(bits uint, lateWindow int64) (*Deduper, error) {
	e, err := NewExtender(bits)
	if err != nil {
		return nil, err
	}
	if lateWindow < 0 {
		return nil, fmt.Errorf("late window must not be negative")
	}
	return &Deduper{ext: e, seen: map[int64]struct{}{}, window: lateWindow}, nil
}

// Ingest records a header and returns its sequence record. A repeated
// extended sequence number is marked Duplicate and must not be counted again.
func (d *Deduper) Ingest(h Header) Sequence {
	ext, _ := d.ext.Extend(uint64(h.Seq))
	s := Sequence{Ext: ext, Header: h}
	if _, ok := d.seen[ext]; ok {
		s.Duplicate = true
		return s
	}
	d.seen[ext] = struct{}{}
	if !d.highInit || ext > d.highExt {
		d.highExt = ext
		d.highInit = true
	}
	if d.highInit && ext < d.highExt {
		s.LateBy = d.highExt - ext
		s.Late = s.LateBy > d.window
	}
	return s
}

// High returns the highest extended sequence observed.
func (d *Deduper) High() int64 { return d.highExt }

// Seen reports whether an extended sequence has been recorded.
func (d *Deduper) Seen(ext int64) bool {
	_, ok := d.seen[ext]
	return ok
}

// SortOrders packets deterministically by extended sequence and arrival
// index. Equal sequence numbers keep arrival order.
func SortOrders(seqs []Sequence) {
	sort.SliceStable(seqs, func(i, j int) bool {
		if seqs[i].Ext != seqs[j].Ext {
			return seqs[i].Ext < seqs[j].Ext
		}
		return seqs[i].Header.Arrival < seqs[j].Header.Arrival
	})
}

// DeserializeRaw decodes the two-byte sequence field without parsing the rest
// of the header, for fixture and log formats that store a raw header blob.
func DeserializeRaw(raw []byte) (uint16, bool) {
	if len(raw) < 4 {
		return 0, false
	}
	return binary.BigEndian.Uint16(raw[2:4]), true
}
