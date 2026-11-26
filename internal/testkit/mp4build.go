package testkit

import (
	"io"

	"github.com/abema/go-mp4"
)

var matrix = [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

// mp4Spec parameterizes a generated fixture.
type mp4Spec struct {
	path      string
	gapAfter  int  // frame index after which a one-frame gap is inserted
	truncated bool // truncate mdat payload at the end
}

// generateMP4 writes a single-track AVC file: ftyp + moov + mdat with one
// sample per chunk, 30 fps, sync samples every second.
func generateMP4(spec mp4Spec) error {
	bw, err := createBoxWriter(spec.path)
	if err != nil {
		return err
	}
	defer bw.close()
	nal := newNALBuilder()

	deltas := make([]mp4.SttsEntry, 0, 4)
	if spec.gapAfter > 0 {
		// frames [0, gapAfter): normal delta; one doubled delta creates the gap.
		deltas = append(deltas,
			mp4.SttsEntry{SampleCount: uint32(spec.gapAfter), SampleDelta: frameDelta},
			mp4.SttsEntry{SampleCount: 1, SampleDelta: frameDelta * 2},
			mp4.SttsEntry{SampleCount: uint32(frameCount - spec.gapAfter - 1), SampleDelta: frameDelta},
		)
	} else {
		deltas = append(deltas, mp4.SttsEntry{
			SampleCount: uint32(frameCount), SampleDelta: frameDelta,
		})
	}
	syncs := make([]uint32, 0, frameCount/keyframeEvery)
	sizes := make([]uint32, frameCount)
	samples := make([][]byte, frameCount)
	for i := 0; i < frameCount; i++ {
		isIDR := i%keyframeEvery == 0
		if isIDR {
			syncs = append(syncs, uint32(i+1)) // stss is 1-based
		}
		samples[i] = nal.sample(isIDR, uint32(i))
		sizes[i] = uint32(len(samples[i]))
	}

	if err := bw.writeFtyp(); err != nil {
		return err
	}
	if err := writeFixtureMoov(bw, deltas, syncs, sizes, nal.avcC(), spec.truncated, samples); err != nil {
		return err
	}
	return nil
}

// writeFixtureMoov builds the minimal moov matching the generated samples.
func writeFixtureMoov(bw *boxWriter, deltas []mp4.SttsEntry, syncs, sizes []uint32,
	avcC []byte, truncated bool, samples [][]byte) error {
	if err := bw.start(mp4.BoxTypeMoov()); err != nil {
		return err
	}
	duration := uint32(frameCount * frameDelta)
	if _, err := mp4.Marshal(bw.f, &mp4.Mvhd{
		Timescale: FixtureTimescale, DurationV0: duration,
		Rate: 0x00010000, Volume: 0x0100, Matrix: matrix, NextTrackID: 2,
	}, mp4.Context{}); err != nil {
		return err
	}
	if err := bw.start(mp4.BoxTypeTrak()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.f, &mp4.Tkhd{
		TrackID: 1, DurationV0: duration, Matrix: matrix,
		Width: uint32(width) << 16, Height: uint32(height) << 16,
	}, mp4.Context{}); err != nil {
		return err
	}
	if err := bw.start(mp4.BoxTypeMdia()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.f, &mp4.Mdhd{
		Timescale: FixtureTimescale, DurationV0: duration,
		Language: [3]byte{'u', 'n', 'd'},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.f, &mp4.Hdlr{
		HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "video",
	}, mp4.Context{}); err != nil {
		return err
	}
	if err := writeFixtureMinf(bw, deltas, syncs, sizes, avcC, truncated, samples); err != nil {
		return err
	}
	// writeFixtureMinf closes minf/mdia/trak/moov after patching co64.
	return nil
}

func writeFixtureMinf(bw *boxWriter, deltas []mp4.SttsEntry, syncs, sizes []uint32,
	avcC []byte, truncated bool, samples [][]byte) error {
	if err := bw.start(mp4.BoxTypeMinf()); err != nil {
		return err
	}
	vmhd := &mp4.Vmhd{}
	vmhd.SetFlags(0x000001)
	if _, err := mp4.Marshal(bw.f, vmhd, mp4.Context{}); err != nil {
		return err
	}
	if err := bw.start(mp4.BoxTypeDinf()); err != nil {
		return err
	}
	if err := bw.start(mp4.BoxTypeDref()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.f, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	url := &mp4.Url{}
	url.SetFlags(0x000001)
	if _, err := mp4.Marshal(bw.f, url, mp4.Context{}); err != nil {
		return err
	}
	if err := bw.end(); err != nil { // dref
		return err
	}
	if err := bw.end(); err != nil { // dinf
		return err
	}
	if err := bw.start(mp4.BoxTypeStbl()); err != nil {
		return err
	}
	if err := writeFixtureStsd(bw, avcC); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.f, &mp4.Stts{
		EntryCount: uint32(len(deltas)), Entries: deltas,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.f, &mp4.Stss{
		EntryCount: uint32(len(syncs)), SampleNumber: syncs,
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.f, &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.f, &mp4.Stsz{
		SampleSize: 0, SampleCount: uint32(len(sizes)), EntrySize: sizes,
	}, mp4.Context{}); err != nil {
		return err
	}
	// One chunk per sample; co64 is reserved here and patched after mdat
	// is written because moov precedes mdat in these fixtures.
	co64Pos, err := reserveCO64(bw, len(sizes))
	if err != nil {
		return err
	}
	if err := bw.end(); err != nil { // stbl
		return err
	}
	if err := bw.end(); err != nil { // minf
		return err
	}
	if err := bw.end(); err != nil { // mdia
		return err
	}
	if err := bw.end(); err != nil { // trak
		return err
	}
	if err := bw.end(); err != nil { // moov
		return err
	}
	return writeFixtureMdat(bw, samples, co64Pos, truncated)
}

// writeFixtureStsd emits avc1 + avcC. The avcC payload is hand-built so
// the fixture stays independent of any real encoder.
func writeFixtureStsd(bw *boxWriter, avcC []byte) error {
	if err := bw.start(mp4.BoxTypeStsd()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(bw.f, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	avc1 := buildAVC1(avcC)
	if err := bw.raw(avc1); err != nil {
		return err
	}
	if err := bw.end(); err != nil { // stsd
		return err
	}
	return nil
}

// writeFixtureMdat writes each sample as its own chunk, records absolute
// offsets, then patches the co64 table. When truncated is set, the final
// sample is cut short so the top-level scan reports a box overrun.
func writeFixtureMdat(bw *boxWriter, samples [][]byte, co64Pos int64, truncated bool) error {
	if err := bw.start(mp4.BoxTypeMdat()); err != nil {
		return err
	}
	offsets := make([]uint64, len(samples))
	for i, s := range samples {
		pos, err := bw.f.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		offsets[i] = uint64(pos)
		if truncated && i == len(samples)-1 {
			if _, err := bw.f.Write(s[:len(s)/2]); err != nil {
				return err
			}
			if err := bw.end(); err != nil {
				return err
			}
			// Deliberately leave co64 unpatched: the declared box runs
			// past EOF and the scan stage must reject the file first.
			return nil
		}
		if _, err := bw.f.Write(s); err != nil {
			return err
		}
	}
	if err := bw.end(); err != nil {
		return err
	}
	return patchCO64(bw, co64Pos, offsets)
}
