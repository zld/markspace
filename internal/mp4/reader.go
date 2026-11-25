package mp4

import (
	"fmt"

	gomp4 "github.com/abema/go-mp4"

	"mediacore/internal/clock"
)

func (f *File) parseMetadata(r readAtSeeker) error {
	moov := f.root.findChild("moov")
	if moov == nil {
		return clock.New(clock.CategoryInvalidBox, f.Path, 0, 0, "missing moov box")
	}
	mvhdBox := moov.findChild("mvhd")
	if mvhdBox == nil {
		return clock.New(clock.CategoryInvalidBox, f.Path, 0, moov.Offset, "missing mvhd box")
	}
	mvhdAny, err := unmarshalBox(r, f.Path, mvhdBox, 0)
	if err != nil {
		return err
	}
	mvhd, ok := mvhdAny.(*gomp4.Mvhd)
	if !ok || mvhd.Timescale == 0 {
		return clock.New(clock.CategoryTimebase, f.Path, 0, mvhdBox.PayloadOffset, "invalid movie timescale")
	}
	f.MovieTimeScale = clock.TicksPerSecond(mvhd.Timescale)
	f.MovieDuration = durationV(mvhd.GetVersion(), mvhd.DurationV0, mvhd.DurationV1)

	trakBoxes := moov.findAll("trak")
	if len(trakBoxes) == 0 {
		return clock.New(clock.CategoryMissingTrack, f.Path, 0, moov.Offset, "moov contains no trak")
	}
	for _, tb := range trakBoxes {
		track, err := parseTrack(r, f.Path, tb, f.MovieTimeScale)
		if err != nil {
			return err
		}
		f.Tracks = append(f.Tracks, track)
	}
	return nil
}

func durationV(version uint8, v0 uint32, v1 uint64) uint64 {
	if version == 1 {
		return v1
	}
	return uint64(v0)
}

func parseTrack(r readAtSeeker, path string, trak *Box, movieScale clock.TicksPerSecond) (*Track, error) {
	number := trak.Track
	if number == 0 {
		number = 1
	}
	t := &Track{Path: path, Number: number, MovieScale: movieScale}
	if tkhdBox := trak.findPath("tkhd"); tkhdBox != nil {
		if any, err := unmarshalBox(r, path, tkhdBox, number); err == nil {
			if tkhd, ok := any.(*gomp4.Tkhd); ok {
				t.ID = tkhd.TrackID
			}
		} else {
			return nil, err
		}
	}
	mdhdBox := trak.findPath("mdia", "mdhd")
	if mdhdBox == nil {
		return nil, clock.New(clock.CategoryInvalidBox, path, number, trak.Offset, "missing mdia/mdhd")
	}
	mdhdAny, err := unmarshalBox(r, path, mdhdBox, number)
	if err != nil {
		return nil, err
	}
	mdhd, ok := mdhdAny.(*gomp4.Mdhd)
	if !ok || mdhd.Timescale == 0 {
		return nil, clock.New(clock.CategoryTimebase, path, number, mdhdBox.PayloadOffset, "invalid track timescale")
	}
	t.TimeScale = clock.TicksPerSecond(mdhd.Timescale)
	t.Duration = durationV(mdhd.GetVersion(), mdhd.DurationV0, mdhd.DurationV1)

	if elstBox := trak.findPath("edts", "elst"); elstBox != nil {
		edits, err := parseEditList(r, path, elstBox, number)
		if err != nil {
			return nil, err
		}
		t.Edits = edits
		t.HasEdit = true
	}

	stbl := trak.findPath("mdia", "minf", "stbl")
	if stbl == nil {
		return nil, clock.New(clock.CategoryInvalidBox, path, number, trak.Offset, "missing stbl sample table")
	}
	if err := parseSampleTables(r, path, number, stbl, t); err != nil {
		return nil, err
	}
	return t, nil
}

func parseSampleTables(r readAtSeeker, path string, number int, stbl *Box, t *Track) error {
	sttsBox := stbl.findChild("stts")
	stszBox := stbl.findChild("stsz")
	stscBox := stbl.findChild("stsc")
	stcoBox := stbl.findChild("stco")
	co64Box := stbl.findChild("co64")
	if sttsBox == nil || stszBox == nil || stscBox == nil || (stcoBox == nil && co64Box == nil) {
		return clock.New(clock.CategoryInvalidBox, path, number, stbl.Offset,
			"stbl is missing one of stts/stsz/stsc/stco")
	}

	dtss, err := decodeDTS(r, path, number, sttsBox)
	if err != nil {
		return err
	}
	ptss, err := decodePTS(r, path, number, stbl.findChild("ctts"), dtss)
	if err != nil {
		return err
	}
	sizes, uniformSize, sampleCount, err := decodeSizes(r, path, number, stszBox)
	if err != nil {
		return err
	}
	if uint32(len(dtss)) != sampleCount {
		return clock.New(clock.CategoryInvalidBox, path, number, sttsBox.PayloadOffset,
			fmt.Sprintf("stts covers %d samples but stsz declares %d", len(dtss), sampleCount))
	}
	sync := map[uint32]bool{}
	var syncNumbers []uint32
	stssBox := stbl.findChild("stss")
	allSyncSamples := stssBox == nil // ISO-BMFF: absent stss means every sample is sync.
	if stssBox != nil {
		any, err := unmarshalBox(r, path, stssBox, number)
		if err != nil {
			return err
		}
		stss, ok := any.(*gomp4.Stss)
		if !ok {
			return clock.New(clock.CategoryInvalidBox, path, number, stssBox.Offset, "invalid stss")
		}
		syncNumbers = append(syncNumbers, stss.SampleNumber...)
		for _, n := range stss.SampleNumber {
			if n == 0 || int(n) > len(dtss) {
				return clock.New(clock.CategoryMissingKeyframe, path, number, stssBox.PayloadOffset+8,
					fmt.Sprintf("stss references sample %d outside 1..%d", n, len(dtss)))
			}
			sync[n] = true
		}
	}

	offsets, err := decodeChunkOffsets(r, path, number, stscBox, stcoBox, co64Box, sampleCount, sizes, uniformSize)
	if err != nil {
		return err
	}
	t.SyncNumbers = syncNumbers
	t.Samples = make([]Sample, sampleCount)
	for i := uint32(0); i < sampleCount; i++ {
		n := int(i + 1)
		size := uniformSize
		if size == 0 {
			size = sizes[i]
		}
		var delta uint32
		// Delta is recoverable from the next DTS; the last entry uses zero.
		if i+1 < sampleCount {
			delta = uint32(dtss[i+1] - dtss[i])
		}
		t.Samples[i] = Sample{
			Number: n, DTS: dtss[i], PTS: ptss[i], Delta: delta,
			Size: size, Offset: offsets[i], Sync: allSyncSamples || sync[uint32(n)],
		}
	}
	return nil
}
