package mediacore

import (
	"io"
	"strconv"

	mp4 "github.com/abema/go-mp4"
)

func boxTypeString(t mp4.BoxType) string {
	var raw [4]byte
	for i := 0; i < 4; i++ {
		raw[i] = t[i]
	}
	for _, ch := range raw {
		if ch < 0x20 || ch > 0x7e {
			return "0x" + strconv.FormatUint(uint64(t[0])<<24|uint64(t[1])<<16|uint64(t[2])<<8|uint64(t[3]), 16)
		}
	}
	return string(raw[:])
}

// parseTrack extracts one trak's metadata and rebuilds its sample table.
func parseTrack(f io.ReadSeeker, trak *mp4.BoxInfo, mdats []BoxRef, opts DemuxOptions) (*Track, error) {
	paths := []mp4.BoxPath{
		{mp4.BoxTypeTkhd()},
		{mp4.BoxTypeEdts(), mp4.BoxTypeElst()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAny()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStts()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCtts()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsc()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsz()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStco()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCo64()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss()},
	}
	boxes, err := mp4.ExtractBoxesWithPayload(f, trak, paths)
	if err != nil {
		return nil, wrapErr(KindContainer, "demux", err)
	}
	if len(boxes) == 0 {
		return nil, fail(KindContainer, "demux", "trak at %d contains no recognised boxes", trak.Offset)
	}
	var (
		tkhd  *mp4.Tkhd
		mdhd  *mp4.Mdhd
		hdlr  *mp4.Hdlr
		entry *mp4.BoxInfoWithPayload
		stts  *mp4.Stts
		ctts  *mp4.Ctts
		stsc  *mp4.Stsc
		stsz  *mp4.Stsz
		stco  *mp4.Stco
		co64  *mp4.Co64
		stss  *mp4.Stss
		elst  *mp4.Elst
	)
	for _, b := range boxes {
		switch p := b.Payload.(type) {
		case *mp4.Tkhd:
			tkhd = p
		case *mp4.Mdhd:
			mdhd = p
		case *mp4.Hdlr:
			hdlr = p
		case *mp4.VisualSampleEntry:
			entry = b
		case *mp4.AudioSampleEntry:
			entry = b
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
		case *mp4.Co64:
			co64 = p
		case *mp4.Stss:
			stss = p
		case *mp4.Elst:
			elst = p
		}
	}
	if tkhd == nil || mdhd == nil || hdlr == nil {
		return nil, fail(KindContainer, "demux", "trak %s missing required boxes (tkhd=%t mdhd=%t hdlr=%t)", boxTypeString(trak.Type), tkhd != nil, mdhd != nil, hdlr != nil)
	}
	if entry == nil {
		return nil, fail(KindContainer, "demux", "track %d has no supported sample entry", tkhd.TrackID)
	}
	if mdhd.Timescale == 0 {
		return nil, fail(KindContainer, "demux", "track %d timescale is zero", tkhd.TrackID)
	}
	if stts == nil || stsc == nil || stsz == nil || (stco == nil && co64 == nil) {
		return nil, fail(KindContainer, "demux", "track %d is missing sample table boxes (stts=%t stsc=%t stsz=%t stco=%t)", tkhd.TrackID, stts != nil, stsc != nil, stsz != nil, stco != nil || co64 != nil)
	}
	if stco != nil && co64 != nil {
		return nil, fail(KindContainer, "demux", "track %d has both stco and co64", tkhd.TrackID)
	}

	tr := &Track{
		ID:            tkhd.TrackID,
		HandlerType:   trimBoxString(hdlr.HandlerType[:]),
		HandlerName:   hdlr.Name,
		Timescale:     mdhd.Timescale,
		DurationTicks: int64(mdhd.GetDuration()),
		Codec:         boxTypeString(entry.Info.Type),
		SampleEntry:   BoxRef{Offset: int64(entry.Info.Offset), Size: int64(entry.Info.Size)},
	}
	switch tr.HandlerType {
	case string(KindVideoTrack):
		tr.Kind = KindVideoTrack
	case string(KindAudioTrack):
		tr.Kind = KindAudioTrack
	default:
		tr.Kind = KindOtherTrack
		tr.Unknown = true
	}
	if vse, ok := entry.Payload.(*mp4.VisualSampleEntry); ok {
		tr.Width, tr.Height = vse.Width, vse.Height
	}
	if ase, ok := entry.Payload.(*mp4.AudioSampleEntry); ok {
		tr.Channels = ase.ChannelCount
	}
	if tr.Unknown && !opts.AllowUnknownTracks {
		return nil, fail(KindContainer, "demux", "track %d has unsupported handler %q", tr.ID, tr.HandlerType)
	}

	sizes, err := sampleSizes(tr, stsz)
	if err != nil {
		return nil, err
	}
	count := len(sizes)
	if count == 0 {
		return nil, fail(KindContainer, "demux", "track %d has no samples", tr.ID)
	}
	tr.Samples = make([]SampleInfo, count)
	for i := range tr.Samples {
		tr.Samples[i] = SampleInfo{Index: i, Size: sizes[i]}
	}

	if err := assignTimestamps(tr, stts, ctts, count); err != nil {
		return nil, err
	}
	chunks, err := chunkPlan(tr.ID, stsc, stco, co64, count)
	if err != nil {
		return nil, err
	}
	if err := assignOffsets(tr, chunks, mdats, opts.ValidateOffsets); err != nil {
		return nil, err
	}
	applySyncSamples(tr, stss)
	applyEditList(tr, elst)
	tr.StartTicks = tr.Samples[0].DTS
	last := tr.Samples[count-1]
	tr.EndTicks = last.DTS + int64(last.Duration)
	return tr, nil
}

func trimBoxString(b []byte) string {
	n := len(b)
	for n > 0 && b[n-1] == ' ' {
		n--
	}
	return string(b[:n])
}

func sampleSizes(tr *Track, stsz *mp4.Stsz) ([]int64, error) {
	if stsz.SampleCount == 0 {
		return nil, fail(KindContainer, "demux", "track %d stsz declares zero samples", tr.ID)
	}
	if stsz.SampleSize != 0 {
		uniform := int64(stsz.SampleSize)
		out := make([]int64, stsz.SampleCount)
		for i := range out {
			out[i] = uniform
		}
		return out, nil
	}
	if int(stsz.SampleCount) != len(stsz.EntrySize) {
		return nil, fail(KindContainer, "demux", "track %d stsz sample count %d does not match %d entries", tr.ID, stsz.SampleCount, len(stsz.EntrySize))
	}
	out := make([]int64, len(stsz.EntrySize))
	for i, v := range stsz.EntrySize {
		out[i] = int64(v)
	}
	return out, nil
}

func assignTimestamps(tr *Track, stts *mp4.Stts, ctts *mp4.Ctts, count int) error {
	var dts int64
	seen := 0
	for _, e := range stts.Entries {
		if e.SampleDelta == 0 {
			return fail(KindTimestamp, "demux", "track %d has a zero sample delta at sample %d (non-increasing timestamps)", tr.ID, seen)
		}
		for i := uint32(0); i < e.SampleCount; i++ {
			if seen >= count {
				return fail(KindContainer, "demux", "track %d stts describes more samples than stsz", tr.ID)
			}
			if i > 0 {
				dts += int64(e.SampleDelta)
			}
			tr.Samples[seen].DTS = dts
			tr.Samples[seen].Duration = e.SampleDelta
			seen++
		}
	}
	if seen != count {
		return fail(KindContainer, "demux", "track %d stts covers %d samples but sample table has %d", tr.ID, seen, count)
	}
	if ctts == nil {
		for i := range tr.Samples {
			tr.Samples[i].CTS = tr.Samples[i].DTS
		}
		return nil
	}
	seen = 0
	for i, e := range ctts.Entries {
		off := ctts.GetSampleOffset(i)
		for n := uint32(0); n < e.SampleCount; n++ {
			if seen >= count {
				return fail(KindContainer, "demux", "track %d ctts describes more samples than stts", tr.ID)
			}
			tr.Samples[seen].CTS = tr.Samples[seen].DTS + off
			seen++
		}
	}
	if seen != count {
		return fail(KindContainer, "demux", "track %d ctts covers %d samples but sample table has %d", tr.ID, seen, count)
	}
	return nil
}

// chunkRef describes the samples belonging to one chunk and where the chunk
// payload starts in the file.
type chunkRef struct {
	Offset int64
	First  int
	Count  int
}

// chunkPlan expands stsc + stco/co64 into a per-chunk sample assignment. It
// verifies that the mapping accounts for every sample exactly once.
func chunkPlan(trackID uint32, stsc *mp4.Stsc, stco *mp4.Stco, co64 *mp4.Co64, sampleCount int) ([]chunkRef, error) {
	var offsets []uint64
	if stco != nil {
		if int(stco.EntryCount) != len(stco.ChunkOffset) {
			return nil, fail(KindContainer, "demux", "track %d stco entry count %d does not match %d offsets", trackID, stco.EntryCount, len(stco.ChunkOffset))
		}
		offsets = make([]uint64, len(stco.ChunkOffset))
		for i, v := range stco.ChunkOffset {
			offsets[i] = uint64(v)
		}
	} else {
		if int(co64.EntryCount) != len(co64.ChunkOffset) {
			return nil, fail(KindContainer, "demux", "track %d co64 entry count %d does not match %d offsets", trackID, co64.EntryCount, len(co64.ChunkOffset))
		}
		offsets = co64.ChunkOffset
	}
	if len(offsets) == 0 {
		return nil, fail(KindContainer, "demux", "track %d has no chunks", trackID)
	}
	if len(stsc.Entries) == 0 {
		return nil, fail(KindContainer, "demux", "track %d stsc has no entries", trackID)
	}
	if int(stsc.EntryCount) != len(stsc.Entries) {
		return nil, fail(KindContainer, "demux", "track %d stsc entry count %d does not match %d entries", trackID, stsc.EntryCount, len(stsc.Entries))
	}

	out := make([]chunkRef, 0, len(offsets))
	sample := 0
	for ci, off := range offsets {
		chunkNumber := uint32(ci + 1)
		// Select the last stsc entry whose FirstChunk <= chunkNumber.
		sel := -1
		for si, e := range stsc.Entries {
			if e.FirstChunk == 0 {
				return nil, fail(KindContainer, "demux", "track %d stsc entry %d has first_chunk 0", trackID, si)
			}
			if e.FirstChunk <= chunkNumber {
				sel = si
			} else {
				break
			}
		}
		if sel < 0 || stsc.Entries[sel].SamplesPerChunk == 0 {
			return nil, fail(KindContainer, "demux", "track %d chunk %d has no sample assignment", trackID, chunkNumber)
		}
		per := int(stsc.Entries[sel].SamplesPerChunk)
		if sample+per > sampleCount {
			return nil, fail(KindContainer, "demux", "track %d chunk mapping exceeds %d samples", trackID, sampleCount)
		}
		out = append(out, chunkRef{Offset: int64(off), First: sample, Count: per})
		sample += per
	}
	if sample != sampleCount {
		return nil, fail(KindContainer, "demux", "track %d chunk mapping covers %d of %d samples", trackID, sample, sampleCount)
	}
	return out, nil
}

func assignOffsets(tr *Track, chunks []chunkRef, mdats []BoxRef, validate bool) error {
	for _, ch := range chunks {
		pos := ch.Offset
		if validate {
			ok := false
			for _, m := range mdats {
				if m.Offset == ch.Offset {
					ok = true
					break
				}
			}
			if !ok {
				return fail(KindContainer, "demux", "track %d chunk payload offset %d is not inside any mdat", tr.ID, ch.Offset)
			}
		}
		for i := ch.First; i < ch.First+ch.Count; i++ {
			s := &tr.Samples[i]
			if pos < 0 {
				return fail(KindContainer, "demux", "track %d sample %d offset overflows", tr.ID, i)
			}
			s.Offset = pos
			if s.Size < 0 {
				return fail(KindContainer, "demux", "track %d sample %d has negative size", tr.ID, i)
			}
			pos += s.Size
			if validate && s.Size > 0 {
				ok := false
				for _, m := range mdats {
					if m.Contains(s.Offset, s.Size) {
						ok = true
						break
					}
				}
				if !ok {
					return fail(KindContainer, "demux", "track %d sample %d range [%d,%d) is outside every mdat", tr.ID, i, s.Offset, s.Offset+s.Size)
				}
			}
		}
	}
	return nil
}

func applySyncSamples(tr *Track, stss *mp4.Stss) {
	if stss == nil {
		// Audio tracks and some encoders are all-sync; absence of stss means
		// every sample is a random-access point.
		for i := range tr.Samples {
			tr.Samples[i].Sync = true
		}
		return
	}
	if int(stss.EntryCount) != len(stss.SampleNumber) {
		// A malformed stss must not silently turn into an all-sync track.
		for i := range tr.Samples {
			tr.Samples[i].Sync = false
		}
		return
	}
	for _, n := range stss.SampleNumber {
		if n == 0 || int(n) > len(tr.Samples) {
			continue
		}
		tr.Samples[n-1].Sync = true
	}
}

// applyEditList records the media-time offset of the first non-empty edit.
// Sample DTS values stay media-relative so remux can reproduce the source
// sample table byte-for-byte; the edit offset is reported separately.
func applyEditList(tr *Track, elst *mp4.Elst) {
	if elst == nil || len(elst.Entries) == 0 || int(elst.EntryCount) != len(elst.Entries) {
		return
	}
	for i := range elst.Entries {
		if elst.GetSegmentDuration(i) == 0 {
			continue // empty edit: leading delay handled by the container
		}
		tr.EditOffsetTicks = elst.GetMediaTime(i)
		return
	}
}
