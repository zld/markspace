package remux

import (
	"encoding/binary"
	"io"

	"github.com/broadcast/mediad/internal/model"
)

// writer is the remux byte builder, driven by adjudicated plans.
type writer struct {
	buf []byte
}

func (w *writer) u8(v byte)       { w.buf = append(w.buf, v) }
func (w *writer) u16(v uint16)    { w.buf = binary.BigEndian.AppendUint16(w.buf, v) }
func (w *writer) u24(v uint32)    { w.buf = append(w.buf, byte(v>>16), byte(v>>8), byte(v)) }
func (w *writer) u32(v uint32)    { w.buf = binary.BigEndian.AppendUint32(w.buf, v) }
func (w *writer) raw(p []byte)    { w.buf = append(w.buf, p...) }
func (w *writer) fourcc(s string) { w.buf = append(w.buf, s[0], s[1], s[2], s[3]) }
func (w *writer) zeros(n int)     { w.buf = append(w.buf, make([]byte, n)...) }

func (w *writer) fullBox(version byte, flags uint32) {
	w.u8(version)
	w.u24(flags)
}

func (w *writer) box(typ string) func() {
	start := len(w.buf)
	w.u32(0)
	w.fourcc(typ)
	return func() {
		binary.BigEndian.PutUint32(w.buf[start:], uint32(len(w.buf)-start))
	}
}

func (w *writer) identity() {
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		w.u32(v)
	}
}

func ftypBytes() []byte {
	f := &writer{}
	closeBox := f.box("ftyp")
	f.fourcc("isom")
	f.u32(0x200)
	f.fourcc("isom")
	f.fourcc("iso2")
	f.fourcc("mp41")
	closeBox()
	return f.buf
}

// trackUnits sums kept sample durations converted into the track timescale.
func trackUnits(g *model.Graph, tp *TrackPlan) uint32 {
	var units uint64
	for _, srcIdx := range tp.Kept {
		s := g.Tracks[tp.TrackID].Samples[srcIdx]
		units += uint64(s.Duration.Num) * uint64(tp.Timescale) / uint64(s.Duration.Den)
	}
	return uint32(units)
}

// Write serializes the plan into ftyp+moov+mdat. Surviving payload bytes are
// copied verbatim from src; isolated samples never appear in the output.
func Write(p *Plan, src io.ReaderAt) ([]byte, error) {
	g := p.graph
	// Pass 1: measure moov with placeholder payloads so mdat offsets are known.
	probe := &writer{}
	if err := probe.writeMoov(g, p, nil, 0); err != nil {
		return nil, err
	}
	ftyp := ftypBytes()
	mdatOffset := uint32(len(ftyp) + len(probe.buf) + 8)

	out := &writer{}
	out.raw(ftyp)
	if err := out.writeMoov(g, p, src, mdatOffset); err != nil {
		return nil, err
	}
	// mdat: append every kept sample's payload in track/sample order.
	closeMdat := out.box("mdat")
	for _, id := range p.Order {
		tp := p.Tracks[id]
		for _, srcIdx := range tp.Kept {
			s := g.Tracks[id].Samples[srcIdx]
			buf, err := readPayload(src, s)
			if err != nil {
				return nil, err
			}
			out.raw(buf)
		}
	}
	closeMdat()
	return out.buf, nil
}

// readPayload returns sample bytes, or deterministic zero placeholders during
// the measurement pass where mdat offsets do not yet exist.
func readPayload(src io.ReaderAt, s *model.Sample) ([]byte, error) {
	if src == nil {
		return make([]byte, s.Size), nil
	}
	buf := make([]byte, s.Size)
	if _, err := src.ReadAt(buf, int64(s.Offset)); err != nil {
		return nil, err
	}
	return buf, nil
}
