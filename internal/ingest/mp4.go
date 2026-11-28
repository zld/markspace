package ingest

import (
	"errors"
	"io"
	"os"

	mp4 "github.com/abema/go-mp4"

	"releasepipe/internal/clock"
	"releasepipe/internal/errs"
)

// Limits guarding against corrupt or hostile containers.
const (
	MaxContainerBytes  = 1 << 30
	MaxSamplesPerTrack = 1 << 20
)

// ReadMP4 parses an MP4 container into tracks with raw per-sample timing
// and source positions, plus container-level metadata for the digest.
func ReadMP4(path string) ([]*Track, map[string]string, error) {
	fail := func(code string, off int64, format string, args ...any) error {
		return errs.New(errs.StageIngest, code, path, off, format, args...)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fail(errs.CodeBadContainer, -1, "%v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, fail(errs.CodeBadContainer, -1, "%v", err)
	}
	fileSize := st.Size()
	if fileSize > MaxContainerBytes {
		return nil, nil, fail(errs.CodeBadContainer, MaxContainerBytes,
			"container too large: %d bytes", fileSize)
	}

	meta := map[string]string{}
	var moov, mdat *mp4.BoxInfo
	for {
		bi, err := mp4.ReadBoxInfo(f)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fail(errs.CodeBadContainer, int64(bi.Offset), "%v", err)
		}
		if bi.Offset+bi.Size > uint64(fileSize) {
			return nil, nil, fail(errs.CodeBadContainer, int64(bi.Offset),
				"box %s extends past end of file", bi.Type)
		}
		switch bi.Type {
		case mp4.BoxTypeFtyp():
			boxes, err := mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{mp4.BoxTypeFtyp()})
			if err != nil || len(boxes) == 0 {
				return nil, nil, fail(errs.CodeBadContainer, int64(bi.Offset), "ftyp: %v", err)
			}
			ft := boxes[0].Payload.(*mp4.Ftyp)
			meta["major_brand"] = string(ft.MajorBrand[:])
			meta["compatible_brands"] = brandList(ft)
		case mp4.BoxTypeMoov():
			moov = bi
		case mp4.BoxTypeMdat():
			mdat = bi
		}
		if _, err := f.Seek(int64(bi.Offset+bi.Size), io.SeekStart); err != nil {
			return nil, nil, fail(errs.CodeBadContainer, int64(bi.Offset), "%v", err)
		}
	}
	if moov == nil {
		return nil, nil, fail(errs.CodeBadContainer, 0, "no moov box")
	}
	if mdat == nil {
		return nil, nil, fail(errs.CodeBadContainer, 0, "no mdat box")
	}
	mdatStart := int64(mdat.Offset + mdat.HeaderSize)
	mdatEnd := int64(mdat.Offset + mdat.Size)
	if mdatEnd > fileSize {
		mdatEnd = fileSize // truncated container: real data ends here
	}

	mvhds, err := mp4.ExtractBoxWithPayload(f, moov, mp4.BoxPath{mp4.BoxTypeMvhd()})
	if err != nil || len(mvhds) == 0 {
		return nil, nil, fail(errs.CodeBadContainer, int64(moov.Offset), "mvhd: %v", err)
	}
	mvhd := mvhds[0].Payload.(*mp4.Mvhd)
	meta["mvhd_timescale"] = itoa(int64(mvhd.Timescale))
	meta["mvhd_duration"] = itoa(int64(mvhd.GetDuration()))

	trakInfos, err := mp4.ExtractBoxes(f, moov, []mp4.BoxPath{{mp4.BoxTypeTrak()}})
	if err != nil {
		return nil, nil, fail(errs.CodeBadContainer, int64(moov.Offset), "trak: %v", err)
	}
	meta["track_count"] = itoa(int64(len(trakInfos)))
	var tracks []*Track
	for i, ti := range trakInfos {
		tr, err := readTrack(f, ti, mdatStart, mdatEnd, meta["major_brand"])
		if err != nil {
			return nil, nil, err
		}
		tr.ID = path + "#" + itoa(int64(i))
		tr.SourcePath = path
		tracks = append(tracks, tr)
	}
	return tracks, meta, nil
}

func readTrack(f *os.File, trak *mp4.BoxInfo, mdatStart, mdatEnd int64, brand string) (*Track, error) {
	fail := func(code string, off int64, format string, args ...any) error {
		return errs.New(errs.StageIngest, code, "trak", off, format, args...)
	}
	stbl := mp4.BoxTypeStbl()
	paths := []mp4.BoxPath{
		{mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), stbl, mp4.BoxTypeStts()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), stbl, mp4.BoxTypeCtts()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), stbl, mp4.BoxTypeStsc()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), stbl, mp4.BoxTypeStsz()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), stbl, mp4.BoxTypeStco()},
	}
	boxes, err := mp4.ExtractBoxesWithPayload(f, trak, paths)
	if err != nil {
		return nil, fail(errs.CodeBadContainer, int64(trak.Offset), "trak payload: %v", err)
	}
	var mdhd *mp4.Mdhd
	var hdlr *mp4.Hdlr
	var stts, ctts, stsc, stsz, stco mp4.IBox
	for _, b := range boxes {
		switch p := b.Payload.(type) {
		case *mp4.Mdhd:
			mdhd = p
		case *mp4.Hdlr:
			hdlr = p
		case *mp4.Stts:
			stts = p
		case *mp4.Ctts:
			ctts = p
		case *mp4.Stsc:
			stsc = p
		case *mp4.Stsz:
			stsz = p
		case *mp4.Stco:
			stco = p
		}
	}
	if mdhd == nil || hdlr == nil || stts == nil || stsz == nil || stco == nil {
		return nil, fail(errs.CodeBadContainer, int64(trak.Offset), "missing required track boxes")
	}
	tb := clock.Timebase{Num: 1, Den: int64(mdhd.Timescale)}
	if !tb.Valid() {
		return nil, fail(errs.CodeBadContainer, int64(trak.Offset), "bad timescale %d", mdhd.Timescale)
	}

	// Expand stts: per-sample DTS and duration (VFR preserved as-is).
	var samples []Sample
	var dts int64
	for _, e := range stts.(*mp4.Stts).Entries {
		for j := uint32(0); j < e.SampleCount; j++ {
			samples = append(samples, Sample{DTS: dts, Duration: int64(e.SampleDelta)})
			dts += int64(e.SampleDelta)
			if len(samples) > MaxSamplesPerTrack {
				return nil, fail(errs.CodeBadContainer, int64(trak.Offset), "too many samples")
			}
		}
	}
	// ctts: PTS = DTS + per-sample composition offset.
	if c, ok := ctts.(*mp4.Ctts); ok && c != nil {
		idx := 0
		for _, e := range c.Entries {
			off := int64(e.SampleOffsetV0)
			if c.Version == 1 {
				off = int64(e.SampleOffsetV1)
			}
			for j := uint32(0); j < e.SampleCount && idx < len(samples); j++ {
				samples[idx].PTS = samples[idx].DTS + off
				idx++
			}
		}
	} else {
		for i := range samples {
			samples[i].PTS = samples[i].DTS
		}
	}
	// stsz: per-sample sizes.
	sz := stsz.(*mp4.Stsz)
	if int(sz.SampleCount) != len(samples) {
		return nil, fail(errs.CodeBadContainer, int64(trak.Offset),
			"stsz count %d != stts count %d", sz.SampleCount, len(samples))
	}
	for i := range samples {
		if sz.SampleSize != 0 {
			samples[i].Size = sz.SampleSize
		} else {
			samples[i].Size = sz.EntrySize[i]
		}
	}
	// stsc + stco: map samples to source byte offsets.
	co := stco.(*mp4.Stco)
	var scEntries []mp4.StscEntry
	if sc, ok := stsc.(*mp4.Stsc); ok && sc != nil {
		scEntries = sc.Entries
	}
	if len(scEntries) == 0 {
		return nil, fail(errs.CodeBadContainer, int64(trak.Offset), "empty stsc")
	}
	si := 0
	for ci := 0; ci < len(co.ChunkOffset) && si < len(samples); ci++ {
		spc := scEntries[len(scEntries)-1].SamplesPerChunk
		for _, e := range scEntries {
			if uint32(ci+1) >= e.FirstChunk {
				spc = e.SamplesPerChunk
			}
		}
		off := int64(co.ChunkOffset[ci])
		for k := uint32(0); k < spc && si < len(samples); k++ {
			samples[si].SourceOffset = off
			off += int64(samples[si].Size)
			si++
		}
	}
	if si != len(samples) {
		return nil, fail(errs.CodeBadContainer, int64(trak.Offset),
			"stsc/stco cover %d of %d samples", si, len(samples))
	}
	// Validate every sample against the mdat payload bounds: a sample
	// whose declared extent falls outside the media data is truncated.
	for i := range samples {
		end := samples[i].SourceOffset + int64(samples[i].Size)
		if samples[i].SourceOffset < mdatStart || end > mdatEnd {
			return nil, fail(errs.CodeTruncatedSample, samples[i].SourceOffset,
				"sample %d [%d,%d) outside mdat payload [%d,%d)",
				i, samples[i].SourceOffset, end, mdatStart, mdatEnd)
		}
	}

	kind := "transition"
	switch string(hdlr.HandlerType[:]) {
	case "vide":
		kind = "video"
	case "soun":
		kind = "audio"
	}
	return &Track{
		Kind:     kind,
		Timebase: tb,
		Samples:  samples,
		Params: map[string]string{
			"handler":      string(hdlr.HandlerType[:]),
			"timescale":    itoa(int64(mdhd.Timescale)),
			"duration":     itoa(int64(mdhd.GetDuration())),
			"sample_count": itoa(int64(len(samples))),
			"major_brand":  brand,
		},
	}, nil
}

func brandList(ft *mp4.Ftyp) string {
	s := ""
	for _, b := range ft.CompatibleBrands {
		if s != "" {
			s += ","
		}
		s += string(b.CompatibleBrand[:])
	}
	return s
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
