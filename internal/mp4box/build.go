package mp4box

// TrackSpec describes one synthetic track.
type TrackSpec struct {
	TrackID    uint32
	Timescale  uint32
	Video      bool
	SampleDur  []uint32 // one delta per sample (stts runs are derived)
	SampleSize []uint32 // one size per sample
	Keyframes  []uint32 // 1-based sample numbers carrying sync samples; nil = all key
	StartPTS   int64    // non-zero media time applied through elst, track ticks
	EmptyHead  uint32   // leading empty-edit duration in movie ticks (0 disables)
}

// Build serialises ftyp+moov(+mdat placeholder) for the given tracks. Sample
// bytes are laid out contiguously; returned chunk offsets point into the mdat
// payload region whose absolute start is reported via MdatOffset.
type BuiltFile struct {
	Data       []byte
	MdatOffset int64 // absolute offset of the first mdat payload byte
	Tracks     []TrackSpec
}

// ftyp and a minimal mvhd keep real parsers happy without carrying data the
// kernel consumes.
func ftypBox() *Box {
	return NewLeaf("ftyp", Concat(
		[]byte("isom"), U32(0x200), []byte("isom"), []byte("iso2"),
	))
}

func mvhdBox() *Box {
	fields := make([]byte, 96) // version-0 mvhd fixed layout
	copy(fields[8:12], U32(1000))
	return NewLeaf("mvhd", FullHeader(0, 0, fields))
}

func mdhdBox(timescale uint32, duration uint32) *Box {
	fields := Concat(
		U32(0), U32(0), // creation/modification
		U32(timescale), U32(duration),
		make([]byte, 4), // language + pre_defined
	)
	return NewLeaf("mdhd", FullHeader(0, 0, fields))
}

func hdlrBox(video bool) *Box {
	htype := []byte("soun")
	if video {
		htype = []byte("vide")
	}
	name := []byte("mediacore\000")
	fields := Concat(U32(0), htype, make([]byte, 12), name)
	return NewLeaf("hdlr", FullHeader(0, 0, fields))
}

func tkhdBox(id uint32) *Box {
	fields := make([]byte, 80)
	copy(fields[8:12], U32(id)) // v0: creation, modification, then track_ID
	return NewLeaf("tkhd", FullHeader(0, 0x7, fields))
}

// stts runs merge consecutive equal deltas.
func sttsBox(durs []uint32) *Box {
	type run struct{ count, delta uint32 }
	var runs []run
	for _, d := range durs {
		if len(runs) > 0 && runs[len(runs)-1].delta == d {
			runs[len(runs)-1].count++
		} else {
			runs = append(runs, run{1, d})
		}
	}
	fields := U32(uint32(len(runs)))
	for _, r := range runs {
		fields = Concat(fields, U32(r.count), U32(r.delta))
	}
	return NewLeaf("stts", FullHeader(0, 0, fields))
}

// stsdBox emits a full box containing one sample-entry child box. The entry
// is opaque to the kernel but must be structurally valid for parsers.
func stsdBox(video bool) *Box {
	entryType := []byte("sowt")
	if video {
		entryType = []byte("raw ")
	}
	entryPayload := Concat(make([]byte, 6), U16(1), make([]byte, 70))
	entryBox := Concat(U32(uint32(8+len(entryPayload))), entryType, entryPayload)
	return NewLeaf("stsd", FullHeader(0, 0, Concat(U32(1), entryBox)))
}

func stszBox(sizes []uint32) *Box {
	fields := Concat(U32(0), U32(uint32(len(sizes))))
	for _, s := range sizes {
		fields = Concat(fields, U32(s))
	}
	return NewLeaf("stsz", FullHeader(0, 0, fields))
}

func stscBox(sampleCount uint32) *Box {
	// every chunk holds one sample: single entry first_chunk=1
	fields := Concat(U32(1), U32(1), U32(1), U32(1))
	_ = sampleCount
	return NewLeaf("stsc", FullHeader(0, 0, fields))
}

func stcoBox(offsets []uint32) *Box {
	fields := U32(uint32(len(offsets)))
	for _, o := range offsets {
		fields = Concat(fields, U32(o))
	}
	return NewLeaf("stco", FullHeader(0, 0, fields))
}

func stssBox(keys []uint32) *Box {
	fields := U32(uint32(len(keys)))
	for _, k := range keys {
		fields = Concat(fields, U32(k))
	}
	return NewLeaf("stss", FullHeader(0, 0, fields))
}

// elstBox builds the edit list. All segment durations are expressed in the
// movie time scale (1000 ticks/s); mediaTime stays in the track time scale.
func elstBox(spec TrackSpec, mediaDurationTrackTicks uint32, timescale uint32) *Box {
	presentMovieTicks := mediaDurationTrackTicks * 1000 / timescale
	type e struct {
		segDur  uint32
		mediaTm int32
	}
	var entries []e
	if spec.EmptyHead > 0 {
		entries = append(entries, e{segDur: spec.EmptyHead, mediaTm: -1})
	}
	entries = append(entries, e{segDur: presentMovieTicks, mediaTm: int32(spec.StartPTS)})
	fields := U32(uint32(len(entries)))
	for _, en := range entries {
		fields = Concat(fields, U32(en.segDur), I32(en.mediaTm), U16(1), U16(0))
	}
	return NewLeaf("elst", FullHeader(0, 0, fields))
}

// Build assembles the file. It first measures the moov size with placeholder
// stco boxes of the final length, then patches the real chunk offsets and
// serialises once.
func Build(tracks []TrackSpec) *BuiltFile {
	placeholders := make([][]uint32, len(tracks))
	for i, tr := range tracks {
		placeholders[i] = make([]uint32, len(tr.SampleSize))
	}
	probe := buildMoov(tracks, placeholders)
	mdatOffset := int64(ftypBox().Size() + probe.Size() + 8)
	offsets := make([][]uint32, len(tracks))
	cursor := uint32(mdatOffset)
	for i, tr := range tracks {
		off := make([]uint32, len(tr.SampleSize))
		for j, sz := range tr.SampleSize {
			off[j] = cursor
			cursor += sz
		}
		offsets[i] = off
	}
	moov := buildMoov(tracks, offsets)
	if moov.Size() != probe.Size() {
		panic("mp4box: moov size changed between probe and final build")
	}
	var w sliceWriter
	_, _ = ftypBox().WriteTo(&w)
	_, _ = moov.WriteTo(&w)
	mdat := NewLeaf("mdat", make([]byte, mdatPayloadSize(tracks)))
	_, _ = mdat.WriteTo(&w)
	return &BuiltFile{Data: []byte(w), MdatOffset: mdatOffset, Tracks: tracks}
}

func mdatPayloadSize(tracks []TrackSpec) int {
	n := 0
	for _, tr := range tracks {
		for _, sz := range tr.SampleSize {
			n += int(sz)
		}
	}
	return n
}

func buildMoov(tracks []TrackSpec, offsets [][]uint32) *Box {
	trakBoxes := make([]*Box, 0, len(tracks))
	for i, tr := range tracks {
		var duration uint32
		for _, d := range tr.SampleDur {
			duration += d
		}
		stblChildren := []*Box{stsdBox(tr.Video), sttsBox(tr.SampleDur), stscBox(uint32(len(tr.SampleSize))), stszBox(tr.SampleSize)}
		if offsets != nil {
			stblChildren = append(stblChildren, stcoBox(offsets[i]))
		}
		if tr.Keyframes != nil {
			stblChildren = append(stblChildren, stssBox(tr.Keyframes))
		}
		stbl := NewContainer("stbl", stblChildren...)
		dref := NewLeaf("dref", FullHeader(0, 0, U32(0)))
		minf := NewContainer("minf", NewContainer("dinf", dref), stbl)
		mdia := NewContainer("mdia", mdhdBox(tr.Timescale, duration), hdlrBox(tr.Video), minf)
		trakChildren := []*Box{tkhdBox(tr.TrackID), mdia}
		if tr.StartPTS != 0 || tr.EmptyHead > 0 {
			trakChildren = append(trakChildren,
				NewContainer("edts", elstBox(tr, duration, tr.Timescale)))
		}
		trakBoxes = append(trakBoxes, NewContainer("trak", trakChildren...))
	}
	return NewContainer("moov", append([]*Box{mvhdBox()}, trakBoxes...)...)
}
