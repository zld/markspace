package media

import (
	"io"
)

// writeRemux emits ftyp/moov/mdat to w and returns the result scaffold.
func writeRemux(w io.WriteSeeker, src io.ReadSeeker, tracks []*TrackInfo, movieTS uint32, brand string) (*RemuxResult, error) {
	plans := make([]*remuxTrackPlan, len(tracks))
	totalSamples := 0
	var mdatSize uint64
	for i, ti := range tracks {
		pl, err := planTrack(src, ti)
		if err != nil {
			return nil, err
		}
		if _, err := RescaleRound(pl.duration, pl.timescale, movieTS); err != nil {
			return nil, NewError(ClassTimebase, "remux", 0, "track %d duration not convertible", ti.TrackID).WithTrack(ti.TrackID)
		}
		plans[i] = pl
		totalSamples += int(ti.SampleCount)
		for _, sz := range ti.Table.Sizes {
			mdatSize += uint64(sz)
		}
	}

	ftyp := buildFtyp(brand)
	if _, err := w.Write(ftyp); err != nil {
		return nil, WrapError(ClassIO, "remux.write", 0, err, "write ftyp")
	}

	// stco entries are fixed width, so the placeholder moov has exactly the
	// same size as the real one. This lets us write the sample data in one
	// forward pass and then patch the moov offsets in place.
	moovOff := len(ftyp)
	moov := buildRemuxMoov(plans, movieTS, nil)
	if _, err := w.Write(moov); err != nil {
		return nil, WrapError(ClassIO, "remux.write", 0, err, "write moov")
	}
	mdatHdr := mdatHeaderSize(mdatSize)
	cursor := uint64(moovOff + len(moov) + mdatHdr)
	if cursor+mdatSize > 0xffffffff {
		return nil, NewError(ClassSizeOverflow, "remux", 0, "output exceeds 32-bit stco range")
	}
	if err := writeMdatHeader(w, mdatSize); err != nil {
		return nil, err
	}

	positions := make([][]uint64, len(plans))
	buf := make([]byte, 64*1024)
	for i, pl := range plans {
		st := pl.src.Table
		offs := make([]uint64, st.Count)
		for k := uint32(0); k < st.Count; k++ {
			s := st.Samples[k]
			offs[k] = cursor
			if _, err := src.Seek(int64(s.Offset), io.SeekStart); err != nil {
				return nil, WrapError(ClassIO, "remux.seek", int64(s.Offset), err, "seek sample")
			}
			n, err := io.CopyBuffer(w, io.LimitReader(src, int64(s.Size)), buf)
			if err != nil {
				return nil, WrapError(ClassIO, "remux.copy", int64(s.Offset), err, "copy sample")
			}
			if n != int64(s.Size) {
				return nil, NewError(ClassTruncated, "remux.copy", int64(s.Offset), "short sample copy: %d of %d", n, s.Size)
			}
			cursor += uint64(s.Size)
		}
		positions[i] = offs
	}

	if _, err := w.Seek(int64(moovOff), io.SeekStart); err != nil {
		return nil, WrapError(ClassIO, "remux.seek", int64(moovOff), err, "seek moov")
	}
	realMoov := buildRemuxMoov(plans, movieTS, positions)
	if len(realMoov) != len(moov) {
		return nil, NewError(ClassIntegrity, "remux.moov", int64(moovOff), "moov size changed between passes")
	}
	if _, err := w.Write(realMoov); err != nil {
		return nil, WrapError(ClassIO, "remux.write", int64(moovOff), err, "rewrite moov")
	}
	if _, err := w.Seek(0, io.SeekEnd); err != nil {
		return nil, WrapError(ClassIO, "remux.seek", 0, err, "seek end")
	}

	res := &RemuxResult{SampleCount: totalSamples}
	for i, pl := range plans {
		res.Tracks = append(res.Tracks, TrackEvidence{
			SourceID: pl.src.TrackID, TargetID: uint32(i + 1), Kind: pl.kind,
			Language: pl.src.Language, SampleCount: pl.src.SampleCount,
		})
	}
	return res, nil
}

func mdatHeaderSize(payload uint64) int {
	if payload+8 > 0xffffffff {
		return 16
	}
	return 8
}

func writeMdatHeader(w io.WriteSeeker, payload uint64) error {
	if mdatHeaderSize(payload) == 16 {
		hdr := &enc{}
		hdr.u32(1).fourcc("mdat").u64(payload + 16)
		if _, err := w.Write(hdr.done()); err != nil {
			return WrapError(ClassIO, "remux.write", 0, err, "write large mdat header")
		}
		return nil
	}
	hdr := &enc{}
	hdr.u32(uint32(payload + 8)).fourcc("mdat")
	if _, err := w.Write(hdr.done()); err != nil {
		return WrapError(ClassIO, "remux.write", 0, err, "write mdat header")
	}
	return nil
}

func buildFtyp(brand string) []byte {
	var compat [][4]byte
	if brand == "qt  " {
		compat = [][4]byte{{'q', 't', ' ', ' '}}
	} else {
		compat = [][4]byte{{'i', 's', 'o', 'm'}, {'i', 's', 'o', '2'}, {'a', 'v', 'c', '1'}, {'m', 'p', '4', '1'}}
	}
	e := &enc{}
	e.fourcc(brand).u32(0x00000200)
	for _, c := range compat {
		e.bytes(c[:])
	}
	return box("ftyp", e.done())
}

func buildRemuxMoov(plans []*remuxTrackPlan, movieTS uint32, positions [][]uint64) []byte {
	var maxDur uint64
	var traks []byte
	for i, pl := range plans {
		mdur, _ := RescaleRound(pl.duration, pl.timescale, movieTS)
		if mdur > maxDur {
			maxDur = mdur
		}
		traks = append(traks, buildRemuxTrak(uint32(i+1), pl, movieTS, positions, i)...)
	}
	mv := mvhdBox(movieTS, maxDur, uint32(len(plans)+1), 0)
	return box("moov", append(mv, traks...))
}

func buildRemuxTrak(targetID uint32, pl *remuxTrackPlan, movieTS uint32, positions [][]uint64, idx int) []byte {
	st := pl.src.Table
	mdur, _ := RescaleRound(pl.duration, pl.timescale, movieTS)
	var children []byte
	children = append(children, tkhdBox(targetID, mdur, pl.width, pl.height, pl.kind == KindAudio, 0)...)
	if len(pl.edits) > 0 {
		children = append(children, box("edts", elstBox(pl.edits))...)
	}
	var mdia []byte
	mdia = append(mdia, mdhdBox(pl.timescale, pl.duration, pl.lang, 0)...)
	if pl.kind == KindAudio {
		mdia = append(mdia, hdlrBox("soun", "SoundHandler")...)
	} else {
		mdia = append(mdia, hdlrBox("vide", "VideoHandler")...)
	}
	var minf []byte
	if pl.kind == KindAudio {
		minf = append(minf, smhdBox()...)
	} else {
		minf = append(minf, vmhdBox()...)
	}
	minf = append(minf, dinfBox()...)
	var stbl []byte
	// pl.stsd is the raw stsd payload (version/flags, entry count, entries),
	// copied verbatim so the codec configuration is bit-identical.
	stbl = append(stbl, box("stsd", pl.stsd)...)
	stbl = append(stbl, sttsBox(st.Durations)...)
	hasCTTS := false
	for _, o := range st.CTS {
		if o != 0 {
			hasCTTS = true
			break
		}
	}
	if hasCTTS {
		stbl = append(stbl, cttsBox(st.CTS)...)
	}
	stbl = append(stbl, stscBox([][3]uint32{{1, 1, 1}})...)
	stbl = append(stbl, stszBox(st.Sizes)...)
	if positions != nil {
		chunk := make([]uint32, st.Count)
		for k, off := range positions[idx] {
			chunk[k] = uint32(off)
		}
		stbl = append(stbl, stcoBox(chunk)...)
	} else {
		stbl = append(stbl, stcoBox(make([]uint32, st.Count))...)
	}
	var syncNums []uint32
	for i, syn := range st.Sync {
		if syn {
			syncNums = append(syncNums, uint32(i+1))
		}
	}
	if len(syncNums) != int(st.Count) {
		stbl = append(stbl, stssBox(syncNums)...)
	}
	minf = append(minf, box("stbl", stbl)...)
	mdia = append(mdia, box("minf", minf)...)
	children = append(children, box("mdia", mdia)...)
	return box("trak", children)
}
