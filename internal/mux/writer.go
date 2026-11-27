// Package mux writes minimal, deterministic MP4/MOV sample-table files used as
// self-contained test fixtures. It is intentionally not a general-purpose
// muxer: the layout is one sample per chunk, fixed sample sizes and a single
// sample description, which is enough to exercise every parsing path in the
// demuxer without depending on external media.
package mux

import (
	"bytes"
	"encoding/binary"
	"io"
)

// SampleSpec describes one sample in fixture time order.
type SampleSpec struct {
	// Delta is the stts duration in track timescale ticks.
	Delta uint32
	// Sync marks a random-access point (emitted in the stss box when true).
	Sync bool
	// Payload is written verbatim into mdat. Nil writes FillBytes of 0xAB.
	Payload []byte
}

// TrackSpec is one media track. Timescale zero defaults to 25.
type TrackSpec struct {
	Timescale uint32
	Video     bool // false writes an audio handler/sample entry
	Samples   []SampleSpec
}

// Faults selects deterministic structural damage for negative tests.
type Faults int

const (
	FaultNone          Faults = iota
	FaultTruncateStbl         // cut the file inside the sample table
	FaultBadSttsCount         // declare more stts entries than are present
	FaultZeroTimescale        // write a zero mdhd timescale
	FaultTruncateStsz         // keep box structure but drop trailing stsz rows
)

// boxWriter accumulates one box with its fourCC header.
type boxWriter struct {
	buf  bytes.Buffer
	hdr  [8]byte
	typ  [4]byte
}

func newBox(typ string) *boxWriter {
	bw := &boxWriter{}
	copy(bw.typ[:], typ)
	return bw
}

func (b *boxWriter) u8(v uint8)             { b.buf.WriteByte(v) }
func (b *boxWriter) u16(v uint16)           { binary.Write(&b.buf, binary.BigEndian, v) }
func (b *boxWriter) u32(v uint32)          { binary.Write(&b.buf, binary.BigEndian, v) }
func (b *boxWriter) u64(v uint64)          { binary.Write(&b.buf, binary.BigEndian, v) }
func (b *boxWriter) bytes(v []byte)         { b.buf.Write(v) }
func (b *boxWriter) zero(n int)             { b.buf.Write(make([]byte, n)) }

// fullBox prefixes one version byte and 24 bits of flags.
func (b *boxWriter) fullBox(flags uint32) {
	b.u8(0)
	b.buf.Write([]byte{byte(flags >> 16), byte(flags >> 8), byte(flags)})
}

// emit writes header (size+type) then payload to w.
func (b *boxWriter) emit(w io.Writer) (uint64, error) {
	payload := b.buf.Bytes()
	binary.BigEndian.PutUint32(b.hdr[:4], uint32(8+len(payload)))
	copy(b.hdr[4:], b.typ[:])
	if _, err := w.Write(b.hdr[:]); err != nil {
		return 0, err
	}
	n, err := w.Write(payload)
	return uint64(n + 8), err
}

// childBox builds an entire sub-box in memory.
func childBox(typ string, fill func(b *boxWriter)) []byte {
	b := newBox(typ)
	fill(b)
	var out bytes.Buffer
	b.emit(&out)
	return out.Bytes()
}

// WriteFile assembles tracks and writes a complete fixture to w.
func WriteFile(w io.Writer, tracks []TrackSpec, fault Faults) (int64, error) {
	f, err := Build(tracks, fault)
	if err != nil {
		return 0, err
	}
	return f.WriteTo(w, fault)
}
