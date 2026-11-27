// Package fixture generates self-contained MP4 sample files — valid,
// truncated, time-regressed and parameter-conflicting — so tests and the
// demo never depend on external media.
package fixture

import (
	"crypto/sha256"
	"fmt"
	"os"

	mp4 "github.com/abema/go-mp4"

	"continuity/media"
)

// TrackSpec describes one track to synthesize.
type TrackSpec struct {
	ID            int
	Video         bool
	Timescale     uint32
	SampleDelta   uint32 // ticks per sample
	SampleCount   int
	SampleSize    int // bytes per sample
	KeyframeEvery int  // video: keyframe interval in samples; 0 = no stss
	Width, Height uint16
	Channels      uint16
	SampleRate    uint32
	Color         media.ColorMeta
	ZeroDeltaAt   int // sample index whose stts delta is forced to 0; -1 = none
}

// Spec describes one file to synthesize.
type Spec struct {
	Tracks       []TrackSpec
	TruncateMoov bool // cut the file off halfway through the moov box
}

var matrix = [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}

// sampleBytes is the deterministic content of one sample.
func sampleBytes(trackID, idx, size int) []byte {
	seed := sha256.Sum256([]byte(fmt.Sprintf("fixture/%d/%d", trackID, idx)))
	out := make([]byte, size)
	for i := range out {
		out[i] = seed[i%len(seed)]
	}
	return out
}

// Generate writes the MP4 file described by spec to path.
func Generate(path string, spec Spec) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := mp4.NewWriter(f)

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}

	// mdat before moov (faststart layout) so chunk offsets are known
	// when the sample tables are written.
	mdat, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()})
	if err != nil {
		return err
	}
	chunkOffset := make([]uint64, len(spec.Tracks))
	for ti, ts := range spec.Tracks {
		pos, err := f.Seek(0, 1)
		if err != nil {
			return err
		}
		chunkOffset[ti] = uint64(pos)
		for i := 0; i < ts.SampleCount; i++ {
			if _, err := f.Write(sampleBytes(ts.ID, i, ts.SampleSize)); err != nil {
				return err
			}
		}
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_ = mdat

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	var totalUnits uint64
	for _, ts := range spec.Tracks {
		totalUnits = max(totalUnits, uint64(ts.SampleCount)*uint64(ts.SampleDelta))
	}
	movieDur := uint32(totalUnits / 90) // 90000 -> 1000 timescale
	if err := writeBox(w, &mp4.Mvhd{
		FullBox:   mp4.FullBox{Version: 0},
		Timescale: 1000, DurationV0: movieDur,
		Rate: 0x10000, Volume: 0x100, Matrix: matrix,
		NextTrackID: uint32(len(spec.Tracks) + 1),
	}); err != nil {
		return err
	}
	for ti, ts := range spec.Tracks {
		if err := writeTrack(w, ts, chunkOffset[ti], movieDur); err != nil {
			return err
		}
	}
	moov, err := w.EndBox() // moov
	if err != nil {
		return err
	}
	if spec.TruncateMoov {
		if err := f.Truncate(int64(moov.Offset + moov.Size/2)); err != nil {
			return err
		}
	}
	return nil
}

func writeBox(w *mp4.Writer, box mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: box.GetType()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}
