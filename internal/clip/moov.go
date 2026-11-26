package clip

import (
	"io"

	"github.com/abema/go-mp4"

	"replay/internal/media"
)

// boxStart/boxEnd wrap the mp4.Writer box nesting.
func boxStart(w *mp4.Writer, t mp4.BoxType) error {
	_, err := w.StartBox(&mp4.BoxInfo{Type: t})
	return err
}

func boxEnd(w *mp4.Writer) error { _, err := w.EndBox(); return err }

func marshal(w io.Writer, b mp4.IImmutableBox) error {
	_, err := mp4.Marshal(w, b, mp4.Context{})
	return err
}

// writeRaw writes a complete on-disk box verbatim (used for stsd entries).
func writeRaw(w io.Writer, raw *media.RawBox) error {
	_, err := w.Write(raw.Data)
	return err
}

// writeFtyp emits a minimal ISO brand header.
func writeFtyp(w *mp4.Writer) error {
	if err := boxStart(w, mp4.BoxTypeFtyp()); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Ftyp{
		MajorBrand: [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}},
		},
	}); err != nil {
		return err
	}
	return boxEnd(w)
}

// writeMoov rebuilds mvhd plus one trak per output track.
func writeMoov(w *mp4.Writer, b *Builder, vt, at *tables) error {
	if err := boxStart(w, mp4.BoxTypeMoov()); err != nil {
		return err
	}
	trackCount := uint32(1)
	if at != nil {
		trackCount = 2
	}
	mvhdDur := scaledDuration(vt, b.Info.Mvhd.Timescale)
	if err := marshal(w, &mp4.Mvhd{
		Timescale: b.Info.Mvhd.Timescale, DurationV0: uint32(mvhdDur),
		Rate: 0x00010000, Volume: 0x0100, Matrix: identityMatrix,
		NextTrackID: trackCount + 1,
	}); err != nil {
		return err
	}
	if err := writeTrak(w, b, vt, 1); err != nil {
		return err
	}
	if at != nil {
		if err := writeTrak(w, b, at, 2); err != nil {
			return err
		}
	}
	return boxEnd(w)
}

// scaledDuration converts a track window duration onto another time base.
func scaledDuration(tb *tables, scale uint32) int64 {
	var ticks int64
	for _, d := range tb.durs {
		ticks += int64(d)
	}
	if scale == tb.track.Timescale {
		return ticks
	}
	return ticks * int64(scale) / int64(tb.track.Timescale)
}

func writeTrak(w *mp4.Writer, b *Builder, tb *tables, trackID uint32) error {
	if err := boxStart(w, mp4.BoxTypeTrak()); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{
		TrackID: trackID, DurationV0: uint32(scaledDuration(tb, b.Info.Mvhd.Timescale)),
		Matrix: identityMatrix,
	}
	src := findTrackBox(b, tb.track.TrackID)
	if tb.track.Handler == media.HandlerVideo {
		tkhd.Width = uint32(src.Width) << 16
		tkhd.Height = uint32(src.Height) << 16
		tkhd.Volume = 0
	} else {
		tkhd.Volume = 0x0100
	}
	if err := marshal(w, tkhd); err != nil {
		return err
	}
	if err := writeMdia(w, b, tb); err != nil {
		return err
	}
	return boxEnd(w)
}

func findTrackBox(b *Builder, trackID uint32) *media.TrackBoxes {
	for _, tb := range b.Info.Tracks {
		if tb.TrackID == trackID {
			return tb
		}
	}
	return nil
}

func writeMdia(w *mp4.Writer, b *Builder, tb *tables) error {
	if err := boxStart(w, mp4.BoxTypeMdia()); err != nil {
		return err
	}
	var dur uint64
	for _, d := range tb.durs {
		dur += uint64(d)
	}
	if err := marshal(w, &mp4.Mdhd{
		Timescale: tb.track.Timescale, DurationV0: uint32(dur),
		Language: [3]byte{'u', 'n', 'd'},
	}); err != nil {
		return err
	}
	handler := string(tb.track.Handler)
	if err := marshal(w, &mp4.Hdlr{
		HandlerType: [4]byte{handler[0], handler[1], handler[2], handler[3]},
		Name:        "replay",
	}); err != nil {
		return err
	}
	if err := writeMinf(w, tb); err != nil {
		return err
	}
	return boxEnd(w)
}

func writeMinf(w *mp4.Writer, tb *tables) error {
	if err := boxStart(w, mp4.BoxTypeMinf()); err != nil {
		return err
	}
	head := mediaHeader(tb)
	if err := marshal(w, head); err != nil {
		return err
	}
	if err := boxStart(w, mp4.BoxTypeDinf()); err != nil {
		return err
	}
	if err := boxStart(w, mp4.BoxTypeDref()); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Dref{EntryCount: 1}); err != nil {
		return err
	}
	url := &mp4.Url{}
	url.SetFlags(0x000001)
	if err := marshal(w, url); err != nil {
		return err
	}
	if err := boxEnd(w); err != nil { // dref
		return err
	}
	if err := boxEnd(w); err != nil { // dinf
		return err
	}
	if err := writeStbl(w, tb); err != nil {
		return err
	}
	return boxEnd(w)
}

func mediaHeader(tb *tables) mp4.IImmutableBox {
	if tb.track.Handler == media.HandlerVideo {
		v := &mp4.Vmhd{}
		v.SetFlags(0x000001)
		return v
	}
	return &mp4.Smhd{}
}

// writeStbl emits stsd (raw sample entry), stts, optional ctts/stss,
// stsc, stsz and the co64 offsets captured while streaming mdat.
func writeStbl(w *mp4.Writer, tb *tables) error {
	if err := boxStart(w, mp4.BoxTypeStbl()); err != nil {
		return err
	}
	if err := boxStart(w, mp4.BoxTypeStsd()); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stsd{EntryCount: 1}); err != nil {
		return err
	}
	if err := writeRaw(w, tb.src.StsdEntry); err != nil {
		return err
	}
	if err := boxEnd(w); err != nil { // stsd
		return err
	}
	if err := marshal(w, &mp4.Stts{EntryCount: uint32(len(tb.stts)), Entries: tb.stts}); err != nil {
		return err
	}
	if len(tb.ctts) > 0 {
		ctts := &mp4.Ctts{EntryCount: uint32(len(tb.ctts)), Entries: tb.ctts}
		ctts.SetVersion(1)
		if err := marshal(w, ctts); err != nil {
			return err
		}
	}
	if len(tb.stss) > 0 {
		if err := marshal(w, &mp4.Stss{
			EntryCount: uint32(len(tb.stss)), SampleNumber: tb.stss,
		}); err != nil {
			return err
		}
	}
	if err := marshal(w, &mp4.Stsc{
		EntryCount: uint32(len(tb.stsc)), Entries: tb.stsc,
	}); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Stsz{
		SampleSize: 0, SampleCount: uint32(len(tb.sizes)), EntrySize: tb.sizes,
	}); err != nil {
		return err
	}
	if err := marshal(w, &mp4.Co64{
		EntryCount: uint32(len(tb.co64)), ChunkOffset: tb.co64,
	}); err != nil {
		return err
	}
	return boxEnd(w)
}
