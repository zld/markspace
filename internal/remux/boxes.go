package remux

import (
	"encoding/binary"
	"io"

	"github.com/abema/go-mp4"

	"mediadist/internal/demux"
	"mediadist/internal/merr"
	"mediadist/internal/timeline"
)

// stcoPatch records where a chunk offset value lives in the file so it can be
// rewritten after mdat placement is known.
type stcoPatch struct {
	fileOffset int64
	track      int
}

// writeFtyp writes the file type box with deterministic brands.
func writeFtyp(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return wrapWrite("ftyp_start", err)
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '5'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '2'}},
		},
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return wrapWrite("ftyp_marshal", err)
	}
	_, err := w.EndBox()
	return wrapWrite("ftyp_end", err)
}

// writeMoov writes moov with placeholder chunk offsets and returns the file
// positions that must be patched once mdat is written.
func writeMoov(w *mp4.Writer, layouts []*layout, opts Options) ([]stcoPatch, error) {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, wrapWrite("moov_start", err)
	}
	if err := writeMvhd(w, layouts, opts); err != nil {
		return nil, err
	}
	var patches []stcoPatch
	for _, l := range layouts {
		// The container track ID is the mapped output ID, not a positional
		// index, so a reader can join MP4 tracks back to the mapping.
		p, err := writeTrak(w, l, l.plan.TrackID)
		if err != nil {
			return nil, err
		}
		patches = append(patches, p...)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, wrapWrite("moov_end", err)
	}
	return patches, nil
}

func writeMvhd(w *mp4.Writer, layouts []*layout, opts Options) error {
	var duration uint64
	for _, l := range layouts {
		scaled, err := timeline.ScaleRound(int64(l.duration), l.plan.Timescale, opts.MovieTimescale)
		if err != nil {
			return merr.Wrap(merr.StageRemux, "duration_scale", merr.NoSpan(0), false, err,
				"track %d duration conversion failed", l.plan.TrackID)
		}
		if uint64(scaled) > duration {
			duration = uint64(scaled)
		}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return wrapWrite("mvhd_start", err)
	}
	mvhd := &mp4.Mvhd{
		Timescale:   opts.MovieTimescale,
		DurationV0:  uint32(duration),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      unityMatrix(),
		NextTrackID: uint32(len(layouts) + 1),
	}
	if _, err := mp4.Marshal(w, mvhd, mp4.Context{}); err != nil {
		return wrapWrite("mvhd_marshal", err)
	}
	_, err := w.EndBox()
	return wrapWrite("mvhd_end", err)
}

func unityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

func writeTrak(w *mp4.Writer, l *layout, trackID uint32) ([]stcoPatch, error) {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return nil, wrapWrite("trak_start", err)
	}
	if err := writeTkhd(w, l, trackID); err != nil {
		return nil, err
	}
	if err := writeEdts(w, l); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return nil, wrapWrite("mdia_start", err)
	}
	if err := writeMdhd(w, l); err != nil {
		return nil, err
	}
	if err := writeHdlr(w, l); err != nil {
		return nil, err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return nil, wrapWrite("minf_start", err)
	}
	if err := writeMediaHeader(w, l); err != nil {
		return nil, err
	}
	if err := writeDinf(w); err != nil {
		return nil, err
	}
	patches, err := writeStbl(w, l)
	if err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return nil, wrapWrite("minf_end", err)
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return nil, wrapWrite("mdia_end", err)
	}
	if _, err := w.EndBox(); err != nil { // trak
		return nil, wrapWrite("trak_end", err)
	}
	return patches, nil
}

func writeTkhd(w *mp4.Writer, l *layout, trackID uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return wrapWrite("tkhd_start", err)
	}
	// tkhd durations are expressed in the movie timescale, not the track's.
	duration := uint32(scaleDuration(l.duration, l.plan.Timescale, l.movieScale))
	tkhd := &mp4.Tkhd{
		TrackID:    trackID,
		DurationV0: duration,
		Matrix:     unityMatrix(),
		Width:      uint32(l.plan.Width) << 16,
		Height:     uint32(l.plan.Height) << 16,
	}
	tkhd.SetFlags(0x000003) // track_enabled | track_in_movie
	if isAudio(l.plan.Codec) {
		tkhd.Volume = 0x0100
	}
	if _, err := mp4.Marshal(w, tkhd, mp4.Context{}); err != nil {
		return wrapWrite("tkhd_marshal", err)
	}
	_, err := w.EndBox()
	return wrapWrite("tkhd_end", err)
}

// writeEdts expresses a non-zero first presentation time. The sample table
// starts at zero, so the correct construction is an empty edit of the initial
// delay followed by a normal edit covering the media. Pointing media_time at
// the original PTS would address beyond the track's duration and play nothing.
func writeEdts(w *mp4.Writer, l *layout) error {
	first := l.samples[0].PTS
	if first == 0 {
		return nil
	}
	delay := scaleDuration(uint64(first), l.plan.Timescale, l.movieScale)
	media := scaleDuration(l.duration, l.plan.Timescale, l.movieScale)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeEdts()}); err != nil {
		return wrapWrite("edts_start", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeElst()}); err != nil {
		return wrapWrite("elst_start", err)
	}
	// Version 1 carries 64-bit fields: a 32-bit RTP timestamp near wrap is
	// ~4.29e9 ticks, which does not fit the version 0 32-bit segment time.
	elst := &mp4.Elst{
		EntryCount: 2,
		Entries: []mp4.ElstEntry{
			// Empty edit: delay the track start. media_time -1 means "no media".
			{SegmentDurationV1: delay, MediaTimeV1: -1, MediaRateInteger: 1},
			// Normal edit: play the whole track from its first sample.
			{SegmentDurationV1: media, MediaTimeV1: 0, MediaRateInteger: 1},
		},
	}
	elst.Version = 1
	if _, err := mp4.Marshal(w, elst, mp4.Context{}); err != nil {
		return wrapWrite("elst_marshal", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapWrite("elst_end", err)
	}
	_, err := w.EndBox()
	return wrapWrite("edts_end", err)
}

func writeMdhd(w *mp4.Writer, l *layout) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return wrapWrite("mdhd_start", err)
	}
	mdhd := &mp4.Mdhd{
		Timescale:  l.plan.Timescale,
		DurationV0: uint32(l.duration),
		Language:   [3]byte{0x15, 0x0E, 0x04}, // "und"
	}
	if _, err := mp4.Marshal(w, mdhd, mp4.Context{}); err != nil {
		return wrapWrite("mdhd_marshal", err)
	}
	_, err := w.EndBox()
	return wrapWrite("mdhd_end", err)
}

func writeHdlr(w *mp4.Writer, l *layout) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return wrapWrite("hdlr_start", err)
	}
	hdlr := &mp4.Hdlr{
		HandlerType: l.plan.Handler,
		Name:        "mediadist-" + string(l.plan.Codec),
	}
	if _, err := mp4.Marshal(w, hdlr, mp4.Context{}); err != nil {
		return wrapWrite("hdlr_marshal", err)
	}
	_, err := w.EndBox()
	return wrapWrite("hdlr_end", err)
}

func writeMediaHeader(w *mp4.Writer, l *layout) error {
	if isAudio(l.plan.Codec) {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeSmhd()}); err != nil {
			return wrapWrite("smhd_start", err)
		}
		if _, err := mp4.Marshal(w, &mp4.Smhd{}, mp4.Context{}); err != nil {
			return wrapWrite("smhd_marshal", err)
		}
		_, err := w.EndBox()
		return wrapWrite("smhd_end", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeVmhd()}); err != nil {
		return wrapWrite("vmhd_start", err)
	}
	vmhd := &mp4.Vmhd{}
	vmhd.SetFlags(1)
	if _, err := mp4.Marshal(w, vmhd, mp4.Context{}); err != nil {
		return wrapWrite("vmhd_marshal", err)
	}
	_, err := w.EndBox()
	return wrapWrite("vmhd_end", err)
}

func writeDinf(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return wrapWrite("dinf_start", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return wrapWrite("dref_start", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return wrapWrite("dref_marshal", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUrl()}); err != nil {
		return wrapWrite("url_start", err)
	}
	url := &mp4.Url{}
	url.SetFlags(1) // media data is in this file
	if _, err := mp4.Marshal(w, url, mp4.Context{}); err != nil {
		return wrapWrite("url_marshal", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapWrite("url_end", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapWrite("dref_end", err)
	}
	_, err := w.EndBox()
	return wrapWrite("dinf_end", err)
}

// writeStbl writes the sample table and returns the stco entry positions.
func writeStbl(w *mp4.Writer, l *layout) ([]stcoPatch, error) {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return nil, wrapWrite("stbl_start", err)
	}
	if err := writeStsd(w, l); err != nil {
		return nil, err
	}
	if err := writeStts(w, l); err != nil {
		return nil, err
	}
	if len(l.stss) > 0 && !isAudio(l.plan.Codec) {
		if err := writeStss(w, l); err != nil {
			return nil, err
		}
	}
	if err := writeStsc(w, l); err != nil {
		return nil, err
	}
	if err := writeStsz(w, l); err != nil {
		return nil, err
	}
	patches, err := writeStco(w, l)
	if err != nil {
		return nil, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, wrapWrite("stbl_end", err)
	}
	return patches, nil
}

func writeStsd(w *mp4.Writer, l *layout) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return wrapWrite("stsd_start", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return wrapWrite("stsd_marshal", err)
	}
	if err := writeSampleEntry(w, l); err != nil {
		return err
	}
	_, err := w.EndBox()
	return wrapWrite("stsd_end", err)
}

func writeSampleEntry(w *mp4.Writer, l *layout) error {
	entryType := l.plan.SampleEntry
	if entryType == [4]byte{} {
		entryType = defaultSampleEntry(l.plan.Codec)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType(entryType)}); err != nil {
		return wrapWrite("sample_entry_start", err)
	}
	if !isAudio(l.plan.Codec) {
		var comp [32]byte
		name := "mediadist"
		comp[0] = byte(len(name))
		copy(comp[1:], name)
		entry := &mp4.VisualSampleEntry{
			SampleEntry: mp4.SampleEntry{
				AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxType(entryType)},
				DataReferenceIndex: 1,
			},
			Width:           l.plan.Width,
			Height:          l.plan.Height,
			Horizresolution: 0x00480000,
			Vertresolution:  0x00480000,
			FrameCount:      1,
			Compressorname:  comp,
			Depth:           0x0018,
			PreDefined3:     -1,
		}
		if _, err := mp4.Marshal(w, entry, mp4.Context{}); err != nil {
			return wrapWrite("sample_entry_marshal", err)
		}
	} else {
		entry := &mp4.AudioSampleEntry{
			SampleEntry: mp4.SampleEntry{
				AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxType(entryType)},
				DataReferenceIndex: 1,
			},
			ChannelCount: 2,
			SampleSize:   16,
			SampleRate:   48000 << 16,
		}
		if _, err := mp4.Marshal(w, entry, mp4.Context{}); err != nil {
			return wrapWrite("sample_entry_marshal", err)
		}
	}
	if len(l.plan.ConfigRecord) > 0 {
		cfgType := l.plan.ConfigEntry
		if cfgType == [4]byte{} {
			cfgType = defaultConfigEntry(l.plan.Codec)
		}
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxType(cfgType)}); err != nil {
			return wrapWrite("config_start", err)
		}
		// The configuration payload is copied byte-for-byte: it is codec
		// metadata, not media, and must not be reinterpreted here.
		if _, err := w.Write(l.plan.ConfigRecord); err != nil {
			return wrapWrite("config_payload", err)
		}
		if _, err := w.EndBox(); err != nil {
			return wrapWrite("config_end", err)
		}
	}
	_, err := w.EndBox()
	return wrapWrite("sample_entry_end", err)
}

func defaultSampleEntry(c demux.Codec) [4]byte {
	switch c {
	case demux.CodecH264:
		return [4]byte{'a', 'v', 'c', '1'}
	case demux.CodecH265:
		return [4]byte{'h', 'e', 'v', '1'}
	case demux.CodecAV1:
		return [4]byte{'a', 'v', '0', '1'}
	case demux.CodecOpus:
		return [4]byte{'O', 'p', 'u', 's'}
	default:
		return [4]byte{'r', 'a', 'w', ' '}
	}
}

func defaultConfigEntry(c demux.Codec) [4]byte {
	switch c {
	case demux.CodecH264:
		return [4]byte{'a', 'v', 'c', 'C'}
	case demux.CodecH265:
		return [4]byte{'h', 'v', 'c', 'C'}
	case demux.CodecAV1:
		return [4]byte{'a', 'v', '1', 'C'}
	case demux.CodecOpus:
		return [4]byte{'d', 'O', 'p', 's'}
	default:
		return [4]byte{}
	}
}

func isAudio(c demux.Codec) bool { return c == demux.CodecOpus }

func writeStts(w *mp4.Writer, l *layout) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return wrapWrite("stts_start", err)
	}
	stts := &mp4.Stts{EntryCount: uint32(len(l.stts)), Entries: l.stts}
	if _, err := mp4.Marshal(w, stts, mp4.Context{}); err != nil {
		return wrapWrite("stts_marshal", err)
	}
	_, err := w.EndBox()
	return wrapWrite("stts_end", err)
}

func writeStss(w *mp4.Writer, l *layout) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
		return wrapWrite("stss_start", err)
	}
	stss := &mp4.Stss{EntryCount: uint32(len(l.stss)), SampleNumber: l.stss}
	if _, err := mp4.Marshal(w, stss, mp4.Context{}); err != nil {
		return wrapWrite("stss_marshal", err)
	}
	_, err := w.EndBox()
	return wrapWrite("stss_end", err)
}

func writeStsc(w *mp4.Writer, l *layout) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return wrapWrite("stsc_start", err)
	}
	entries := []mp4.StscEntry{{
		FirstChunk:             1,
		SamplesPerChunk:        uint32(len(l.sizes)),
		SampleDescriptionIndex: 1,
	}}
	stsc := &mp4.Stsc{EntryCount: uint32(len(entries)), Entries: entries}
	if _, err := mp4.Marshal(w, stsc, mp4.Context{}); err != nil {
		return wrapWrite("stsc_marshal", err)
	}
	_, err := w.EndBox()
	return wrapWrite("stsc_end", err)
}

func writeStsz(w *mp4.Writer, l *layout) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return wrapWrite("stsz_start", err)
	}
	stsz := &mp4.Stsz{
		SampleSize:  0,
		SampleCount: uint32(len(l.sizes)),
		EntrySize:   l.sizes,
	}
	if _, err := mp4.Marshal(w, stsz, mp4.Context{}); err != nil {
		return wrapWrite("stsz_marshal", err)
	}
	_, err := w.EndBox()
	return wrapWrite("stsz_end", err)
}

// writeStco writes one chunk per track and returns the file position of the
// single offset value so it can be patched after mdat is placed.
func writeStco(w *mp4.Writer, l *layout) ([]stcoPatch, error) {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()})
	if err != nil {
		return nil, wrapWrite("stco_start", err)
	}
	stco := &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{0}}
	if _, err := mp4.Marshal(w, stco, mp4.Context{}); err != nil {
		return nil, wrapWrite("stco_marshal", err)
	}
	// Marshal wrote: box header (8) + version/flags (4) + entry_count (4) +
	// one uint32 offset. Record where that offset starts.
	patchPos := int64(bi.Offset + bi.HeaderSize + 8)
	if _, err := w.EndBox(); err != nil {
		return nil, wrapWrite("stco_end", err)
	}
	return []stcoPatch{{fileOffset: patchPos}}, nil
}

// writeMdat writes the media data box and patches chunk offsets in place.
func writeMdat(f io.WriteSeeker, w *mp4.Writer, layouts []*layout, patches []stcoPatch) error {
	mdatType := mp4.BoxTypeMdat()
	cur, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return wrapWrite("mdat_tell", err)
	}
	var total int64
	for _, l := range layouts {
		total += int64(len(l.bytes))
	}
	hdr := make([]byte, 8)
	useLarge := total+8 > int64(^uint32(0))
	if useLarge {
		hdr = make([]byte, 16)
		binary.BigEndian.PutUint32(hdr[0:4], 1)
		copy(hdr[4:8], mdatType[:])
		binary.BigEndian.PutUint64(hdr[8:16], uint64(total)+16)
	} else {
		binary.BigEndian.PutUint32(hdr[0:4], uint32(total)+8)
		copy(hdr[4:8], mdatType[:])
	}
	if _, err := f.Write(hdr); err != nil {
		return wrapWrite("mdat_header", err)
	}
	payloadStart := cur + int64(len(hdr))
	cursor := payloadStart
	for i, l := range layouts {
		if i < len(patches) {
			if _, err := f.Seek(patches[i].fileOffset, io.SeekStart); err != nil {
				return wrapWrite("stco_patch_seek", err)
			}
			var off [4]byte
			binary.BigEndian.PutUint32(off[:], uint32(cursor))
			if _, err := f.Write(off[:]); err != nil {
				return wrapWrite("stco_patch_write", err)
			}
			if _, err := f.Seek(cursor, io.SeekStart); err != nil {
				return wrapWrite("mdat_reseek", err)
			}
		}
		if _, err := f.Write(l.bytes); err != nil {
			return wrapWrite("mdat_payload", err)
		}
		// Record per-sample MP4 offsets for evidence.
		l.offsets = l.offsets[:0]
		pos := cursor
		for _, sz := range l.sizes {
			l.offsets = append(l.offsets, uint32(pos))
			pos += int64(sz)
		}
		l.chunkOffset = uint32(cursor)
		cursor += int64(len(l.bytes))
	}
	_, err = f.Seek(cursor, io.SeekStart)
	if err != nil {
		return wrapWrite("mdat_final_seek", err)
	}
	return nil
}

func collectEvidence(layouts []*layout) []SampleEvidence {
	var out []SampleEvidence
	for _, l := range layouts {
		for i, s := range l.samples {
			ev := SampleEvidence{
				TrackID:   l.plan.TrackID,
				Index:     i,
				PTS:       s.PTS,
				Dur:       s.Dur,
				Size:      int(l.sizes[i]),
				SHA256:    hashOf(s.Data),
				CanonHash: l.canon[i],
				Keyframe:  s.Keyframe,
				Damaged:   s.Damaged,
				Segment:   s.Segment,
			}
			if i < len(l.offsets) {
				ev.MP4Offset = int64(l.offsets[i])
			}
			out = append(out, ev)
		}
	}
	return out
}

// scaleDuration converts a duration between timescales, rounding up so a
// container duration never understates the media it describes.
func scaleDuration(duration uint64, from, to uint32) uint64 {
	if from == 0 || to == 0 || from == to {
		return duration
	}
	scaled, err := timeline.Scale(int64(duration), from, to)
	if err != nil {
		return duration
	}
	return uint64(scaled)
}
