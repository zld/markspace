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
// extended space. Each new value is placed relative to the previous one using
// the signed distance modulo the wrap modulus, which keeps every step smaller
// than half a cycle (the standard RTP closest-extension rule).
type Extender struct {
	bits    uint
	modulus uint64
	half    uint64
	lastRaw uint64
	lastExt int64
	init    bool
}

// NewExtender builds an extender for values of the given bit width (1..63).
func NewExtender(bits uint) (*Extender, error) {
	if bits == 0 || bits > 63 {
		return nil, fmt.Errorf("wrap width must be in 1..63, got %d", bits)
	}
	return &Extender{bits: bits, modulus: uint64(1) << bits, half: uint64(1) << (bits - 1)}, nil
}

// Bits returns the configured bit width.
func (e *Extender) Bits() uint { return e.bits }

// Reset forgets the current reference.
func (e *Extender) Reset() {
	e.lastRaw = 0
	e.lastExt = 0
	e.init = false
}

// Extend maps a raw value into the extended space. The first value
// establishes the base at its own magnitude. The second result reports
// whether the value landed before the previous one (an out-of-order arrival).
func (e *Extender) Extend(raw uint64) (ext int64, backward bool) {
	raw &= e.modulus - 1
	if !e.init {
		e.init = true
		e.lastRaw = raw
		e.lastExt = int64(raw)
		return e.lastExt, false
	}
	delta := (raw - e.lastRaw) & (e.modulus - 1)
	d := int64(delta)
	if delta >= e.half {
		d = int64(delta) - int64(e.modulus)
	}
	prev := e.lastExt
	ext = prev + d
	e.lastRaw = raw
	e.lastExt = ext
	return ext, ext < prev
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

// ExtRange is an inclusive range of extended sequence numbers.
type ExtRange struct {
	Lo int64 `json:"lo"`
	Hi int64 `json:"hi"`
}

// DeduperSnapshot is the serializable deduplication state.
type DeduperSnapshot struct {
	Extender ExtenderSnapshot `json:"extender"`
	Seen     []ExtRange       `json:"seen"`
}

// Snapshot captures the deduper state. The seen set is compressed into
// inclusive ranges, which stays small even for long runs with many losses.
func (d *Deduper) Snapshot() DeduperSnapshot {
	exts := make([]int64, 0, len(d.seen))
	for e := range d.seen {
		exts = append(exts, e)
	}
	sort.Slice(exts, func(i, j int) bool { return exts[i] < exts[j] })
	ranges := make([]ExtRange, 0, len(exts))
	for _, e := range exts {
		if n := len(ranges); n > 0 && ranges[n-1].Hi == e-1 {
			ranges[n-1].Hi = e
			continue
		}
		ranges = append(ranges, ExtRange{Lo: e, Hi: e})
	}
	return DeduperSnapshot{Extender: d.ext.Snapshot(), Seen: ranges}
}

// Restore replaces the deduper state.
func (d *Deduper) Restore(s DeduperSnapshot) {
	d.ext.Restore(s.Extender)
	d.seen = make(map[int64]struct{}, len(s.Seen))
	for _, r := range s.Seen {
		for e := r.Lo; e <= r.Hi; e++ {
			d.seen[e] = struct{}{}
		}
	}
	d.highExt = 0
	d.highInit = false
	if len(s.Seen) > 0 {
		d.highExt = s.Seen[len(s.Seen)-1].Hi
		d.highInit = true
	}
}

// Count returns the number of distinct sequence numbers recorded.
func (d *Deduper) Count() int { return len(d.seen) }

// RelativeExt maps a raw sequence number to the extended space relative to the
// most recent packet, assuming it lies within half a wrap cycle. It is used
// for redundancy records that name earlier sequence numbers.
func (d *Deduper) RelativeExt(raw uint64) int64 {
	mod := d.ext.modulus
	raw &= mod - 1
	if !d.ext.init {
		return int64(raw)
	}
	behind := (d.ext.lastRaw - raw) & (mod - 1)
	if behind >= d.ext.half {
		behind -= mod // signed distance: raw is ahead of the last packet
	}
	return d.ext.lastExt - int64(behind)
}

// SnapTo moves the extender's reference to an already extended sequence
// value. It is used when resuming from a checkpoint, where only extended
// values were retained.
func (d *Deduper) SnapTo(ext int64) {
	d.ext.init = true
	d.ext.lastExt = ext
	d.ext.lastRaw = uint64(ext) & (d.ext.modulus - 1)
	if !d.highInit || ext > d.highExt {
		d.highExt = ext
		d.highInit = true
	}
	d.seen[ext] = struct{}{}
}
