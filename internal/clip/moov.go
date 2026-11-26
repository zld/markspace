package clip

import (
	"os"

	"github.com/abema/go-mp4"

	"replay/internal/media"
)

// Every container box uses openContainer/close so its size is patched
// after its children are written; every leaf box goes through marshalLeaf
// (go-mp4 Marshal), which emits a complete, correctly sized box.

func writeRaw(f *os.File, raw *media.RawBox) error {
	_, err := f.Write(raw.Data)
	return err
}

// writeFtyp emits a minimal ISO brand header as a self-contained leaf.
func writeFtyp(f *os.File) error {
	return marshalLeaf(f, &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}},
		},
	})
}

// writeMoov rebuilds mvhd plus one trak per output track.
func writeMoov(f *os.File, b *Builder, vt, at *tables) error {
	cw, err := openContainer(f, "moov")
	if err != nil {
		return err
	}
	trackCount := uint32(1)
	if at != nil {
		trackCount = 2
	}
	mvhdDur := scaledDuration(vt, b.Info.Mvhd.Timescale)
	if err := marshalLeaf(f, &mp4.Mvhd{
		Timescale: b.Info.Mvhd.Timescale, DurationV0: uint32(mvhdDur),
		Rate: 0x00010000, Volume: 0x0100, Matrix: identityMatrix,
		NextTrackID: trackCount + 1,
	}); err != nil {
		return err
	}
	if err := writeTrak(f, b, vt, 1); err != nil {
		return err
	}
	if at != nil {
		if err := writeTrak(f, b, at, 2); err != nil {
			return err
		}
	}
	return cw.close()
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

func writeTrak(f *os.File, b *Builder, tb *tables, trackID uint32) error {
	cw, err := openContainer(f, "trak")
	if err != nil {
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
	} else {
		tkhd.Volume = 0x0100
	}
	if err := marshalLeaf(f, tkhd); err != nil {
		return err
	}
	if err := writeMdia(f, b, tb); err != nil {
		return err
	}
	return cw.close()
}

func findTrackBox(b *Builder, trackID uint32) *media.TrackBoxes {
	for _, tb := range b.Info.Tracks {
		if tb.TrackID == trackID {
			return tb
		}
	}
	return nil
}

func writeMdia(f *os.File, b *Builder, tb *tables) error {
	cw, err := openContainer(f, "mdia")
	if err != nil {
		return err
	}
	var dur uint64
	for _, d := range tb.durs {
		dur += uint64(d)
	}
	if err := marshalLeaf(f, &mp4.Mdhd{
		Timescale: tb.track.Timescale, DurationV0: uint32(dur),
		Language: [3]byte{'u', 'n', 'd'},
	}); err != nil {
		return err
	}
	handler := string(tb.track.Handler)
	if err := marshalLeaf(f, &mp4.Hdlr{
		HandlerType: [4]byte{handler[0], handler[1], handler[2], handler[3]},
		Name:        "replay",
	}); err != nil {
		return err
	}
	if err := writeMinf(f, tb); err != nil {
		return err
	}
	return cw.close()
}

func writeMinf(f *os.File, tb *tables) error {
	cw, err := openContainer(f, "minf")
	if err != nil {
		return err
	}
	if tb.track.Handler == media.HandlerVideo {
		vmhd := &mp4.Vmhd{}
		vmhd.SetFlags(0x000001)
		if err := marshalLeaf(f, vmhd); err != nil {
			return err
		}
	} else {
		if err := marshalLeaf(f, &mp4.Smhd{}); err != nil {
			return err
		}
	}
	dinf, err := openContainer(f, "dinf")
	if err != nil {
		return err
	}
	dref, err := openFullContainer(f, "dref", 0, 0)
	if err != nil {
		return err
	}
	// entry_count=1 then the self-contained url leaf box.
	if err := writeU32(f, 1); err != nil {
		return err
	}
	url := &mp4.Url{}
	url.SetFlags(0x000001)
	if err := marshalLeaf(f, url); err != nil {
		return err
	}
	if err := dref.close(); err != nil {
		return err
	}
	if err := dinf.close(); err != nil {
		return err
	}
	if err := writeStbl(f, tb); err != nil {
		return err
	}
	return cw.close()
}

// writeStbl emits stsd (raw sample entry), stts, optional ctts/stss,
// stsc, stsz and the co64 offsets captured while streaming mdat.
func writeStbl(f *os.File, tb *tables) error {
	cw, err := openContainer(f, "stbl")
	if err != nil {
		return err
	}
	stsd, err := openFullContainer(f, "stsd", 0, 0)
	if err != nil {
		return err
	}
	if err := writeU32(f, 1); err != nil { // entry_count
		return err
	}
	if err := writeRaw(f, tb.src.StsdEntry); err != nil {
		return err
	}
	if err := stsd.close(); err != nil {
		return err
	}
	if err := marshalLeaf(f, &mp4.Stts{EntryCount: uint32(len(tb.stts)), Entries: tb.stts}); err != nil {
		return err
	}
	if len(tb.ctts) > 0 {
		ctts := &mp4.Ctts{EntryCount: uint32(len(tb.ctts)), Entries: tb.ctts}
		ctts.SetVersion(1)
		if err := marshalLeaf(f, ctts); err != nil {
			return err
		}
	}
	if len(tb.stss) > 0 {
		if err := marshalLeaf(f, &mp4.Stss{
			EntryCount: uint32(len(tb.stss)), SampleNumber: tb.stss,
		}); err != nil {
			return err
		}
	}
	if err := marshalLeaf(f, &mp4.Stsc{
		EntryCount: uint32(len(tb.stsc)), Entries: tb.stsc,
	}); err != nil {
		return err
	}
	if err := marshalLeaf(f, &mp4.Stsz{
		SampleSize: 0, SampleCount: uint32(len(tb.sizes)), EntrySize: tb.sizes,
	}); err != nil {
		return err
	}
	if err := marshalLeaf(f, &mp4.Co64{
		EntryCount: uint32(len(tb.co64)), ChunkOffset: tb.co64,
	}); err != nil {
		return err
	}
	return cw.close()
}
