package remux

import (
	"fmt"
	"io"

	"github.com/broadcast/mediad/internal/model"
)

func (w *writer) writeMoov(g *model.Graph, p *Plan, src io.ReaderAt, base map[uint32]uint64) error {
	closeMoov := w.box("moov")
	if err := w.writeMvhd(g, p); err != nil {
		return err
	}
	for _, id := range p.Order {
		if err := w.writeTrak(g, p, p.Tracks[id], src, base); err != nil {
			return err
		}
	}
	closeMoov()
	return nil
}

func (w *writer) writeMvhd(g *model.Graph, p *Plan) error {
	closeBox := w.box("mvhd")
	w.fullBox(0, 0)
	w.u32(0)
	w.u32(0)
	w.u32(1000)
	var maxDur uint32
	for _, id := range p.Order {
		tp := p.Tracks[id]
		scaled := uint64(trackUnits(g, tp)) * 1000 / uint64(tp.Timescale)
		if uint32(scaled) > maxDur {
			maxDur = uint32(scaled)
		}
	}
	w.u32(maxDur)
	w.u32(0x00010000)
	w.u16(0x0100)
	w.zeros(10)
	w.identity()
	w.zeros(24)
	w.u32(uint32(len(p.Order)) + 1)
	closeBox()
	return nil
}

func (w *writer) writeTrak(g *model.Graph, p *Plan, tp *TrackPlan, src io.ReaderAt, base map[uint32]uint64) error {
	closeTrak := w.box("trak")
	w.writeTkhd(g, tp)
	if err := w.writeMdia(g, p, tp, src, base); err != nil {
		return err
	}
	closeTrak()
	return nil
}

func (w *writer) writeTkhd(g *model.Graph, tp *TrackPlan) {
	closeBox := w.box("tkhd")
	w.fullBox(0, 0x000003)
	w.u32(0)
	w.u32(0)
	w.u32(tp.TrackID)
	w.u32(0)
	w.u32(trackUnits(g, tp))
	w.zeros(8)
	w.u16(0)
	w.u16(0)
	if tp.Kind == model.KindAudio {
		w.u16(0x0100)
	} else {
		w.u16(0)
	}
	w.u16(0)
	w.identity()
	if tp.Kind == model.KindVideo && tp.Video != nil {
		w.u32(uint32(tp.Video.Width) << 16)
		w.u32(uint32(tp.Video.Height) << 16)
	} else {
		w.zeros(8)
	}
	closeBox()
}

func (w *writer) writeMdia(g *model.Graph, p *Plan, tp *TrackPlan, src io.ReaderAt, base map[uint32]uint64) error {
	closeMdia := w.box("mdia")
	w.writeMdhd(g, tp)
	w.writeHdlr(tp)
	if err := w.writeMinf(g, p, tp, src, base); err != nil {
		return err
	}
	closeMdia()
	return nil
}

func (w *writer) writeMdhd(g *model.Graph, tp *TrackPlan) {
	closeBox := w.box("mdhd")
	w.fullBox(0, 0)
	w.u32(0)
	w.u32(0)
	w.u32(tp.Timescale)
	w.u32(trackUnits(g, tp))
	w.u16(0x55C4)
	w.u16(0)
	closeBox()
}

func (w *writer) writeHdlr(tp *TrackPlan) {
	closeBox := w.box("hdlr")
	w.fullBox(0, 0)
	w.u32(0)
	switch tp.Kind {
	case model.KindVideo:
		w.fourcc("vide")
	case model.KindAudio:
		w.fourcc("soun")
	default:
		w.fourcc("meta")
	}
	w.zeros(12)
	w.raw(append([]byte("mediad remux"), 0))
	closeBox()
}

func (w *writer) writeMinf(g *model.Graph, p *Plan, tp *TrackPlan, src io.ReaderAt, base map[uint32]uint64) error {
	closeMinf := w.box("minf")
	if tp.Kind == model.KindVideo {
		closeVmhd := w.box("vmhd")
		w.fullBox(0, 1)
		w.zeros(8)
		closeVmhd()
	} else {
		closeSmhd := w.box("smhd")
		w.fullBox(0, 0)
		w.zeros(4)
		closeSmhd()
	}
	closeDinf := w.box("dinf")
	closeDref := w.box("dref")
	w.fullBox(0, 0)
	w.u32(1)
	closeUrl := w.box("url ")
	w.fullBox(0, 1)
	closeUrl()
	closeDref()
	closeDinf()
	if err := w.writeStbl(g, tp, base[tp.TrackID]); err != nil {
		return err
	}
	closeMinf()
	return nil
}

func (w *writer) writeStbl(g *model.Graph, tp *TrackPlan, firstOffset uint64) error {
	closeStbl := w.box("stbl")
	if err := w.writeStsd(tp); err != nil {
		return err
	}
	if err := w.writeSttsStscStsz(g, tp); err != nil {
		return err
	}
	if err := w.writeStco(g, tp, firstOffset); err != nil {
		return err
	}
	if tp.HasStss && len(tp.Keyframes) > 0 {
		closeStss := w.box("stss")
		w.fullBox(0, 0)
		w.u32(uint32(len(tp.Keyframes)))
		for _, rel := range tp.Keyframes {
			w.u32(uint32(rel + 1)) // stss is 1-based
		}
		closeStss()
	}
	closeStbl()
	return nil
}

// writeStco emits one chunk (one sample) per kept sample. Offsets walk mdat
// sequentially across tracks in plan order, matching Write's payload order.
func (w *writer) writeStco(g *model.Graph, tp *TrackPlan, firstOffset uint64) error {
	closeBox := w.box("stco")
	w.fullBox(0, 0)
	w.u32(uint32(len(tp.Kept)))
	cursor := uint64(firstOffset)
	for _, srcIdx := range tp.Kept {
		w.u32(uint32(cursor))
		cursor += uint64(g.Tracks[tp.TrackID].Samples[srcIdx].Size)
	}
	closeBox()
	return nil
}

func (w *writer) writeStsd(tp *TrackPlan) error {
	closeStsd := w.box("stsd")
	w.fullBox(0, 0)
	w.u32(1)
	switch tp.Kind {
	case model.KindVideo:
		if tp.Video == nil {
			return fmt.Errorf("video track %d missing format metadata", tp.TrackID)
		}
		closeEntry := w.box(tp.Video.Codec)
		w.zeros(6)
		w.u16(1)
		w.u16(0)
		w.u16(0)
		w.zeros(12)
		w.u16(tp.Video.Width)
		w.u16(tp.Video.Height)
		w.u32(0x00480000)
		w.u32(0x00480000)
		w.u32(0)
		w.u16(1)
		w.zeros(32)
		w.u16(0x0018)
		w.u16(0xFFFF)
		if tp.Video.HasColorMetadata {
			closeColr := w.box("colr")
			w.fourcc(tp.Video.ColourType)
			w.u16(tp.Video.ColourPrimaries)
			w.u16(tp.Video.Transfer)
			w.u16(tp.Video.Matrix)
			w.u8(0)
			closeColr()
		}
		closeEntry()
	case model.KindAudio:
		if tp.Audio == nil {
			return fmt.Errorf("audio track %d missing format metadata", tp.TrackID)
		}
		closeEntry := w.box(tp.Audio.Codec)
		w.zeros(6)
		w.u16(1)
		w.zeros(8)
		w.u16(tp.Audio.ChannelCount)
		w.u16(tp.Audio.SampleSize)
		w.u16(0)
		w.u16(0)
		w.u32(tp.Audio.SampleRate << 16)
		closeEntry()
	default:
		return fmt.Errorf("unsupported track kind %s", tp.Kind)
	}
	closeStsd()
	return nil
}

func (w *writer) writeSttsStscStsz(g *model.Graph, tp *TrackPlan) error {
	type run struct{ count, delta uint32 }
	var runs []run
	for _, srcIdx := range tp.Kept {
		s := g.Tracks[tp.TrackID].Samples[srcIdx]
		units := uint32(s.Duration.Num) * tp.Timescale / uint32(s.Duration.Den)
		if n := len(runs); n > 0 && runs[n-1].delta == units {
			runs[n-1].count++
		} else {
			runs = append(runs, run{1, units})
		}
	}
	closeStts := w.box("stts")
	w.fullBox(0, 0)
	w.u32(uint32(len(runs)))
	for _, r := range runs {
		w.u32(r.count)
		w.u32(r.delta)
	}
	closeStts()

	closeStsc := w.box("stsc")
	w.fullBox(0, 0)
	w.u32(1)
	w.u32(1)
	w.u32(1)
	w.u32(1)
	closeStsc()

	closeStsz := w.box("stsz")
	w.fullBox(0, 0)
	w.u32(0)
	w.u32(uint32(len(tp.Kept)))
	for _, srcIdx := range tp.Kept {
		w.u32(g.Tracks[tp.TrackID].Samples[srcIdx].Size)
	}
	closeStsz()
	return nil
}
