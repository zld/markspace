package container

import (
	"bytes"
	"fmt"

	"github.com/abema/go-mp4"

	"mediascan/internal/clock"
	"mediascan/internal/errs"
)

// Track is the validated view of one moov/trak subtree.
type Track struct {
	TrackID   uint32
	Handler   string // "vide", "soun", ...
	Timescale uint32 // mdhd timescale
	Duration  int64  // mdhd duration in track ticks
	Samples   uint32 // stsz sample count
	Keyframes []uint32
	// ChunkRanges are stco/co64 chunk offsets validated against file size.
	ChunkRanges []ByteSpan
	// SampleRanges are per-sample byte spans derived from stsz/stsc/stco.
	SampleRanges []ByteSpan
	// UnknownKeyframe marks tracks whose keyframe index cannot be confirmed.
	UnknownKeyframe bool
}

// ByteSpan is a half-open [Offset, Offset+Length) interval inside the file.
type ByteSpan struct {
	Offset int64
	Length int64
}

// End returns the exclusive end offset.
func (s ByteSpan) End() int64 { return s.Offset + s.Length }

// extractTracks locates every moov/trak then pulls its typed leaf boxes
// with go-mp4's ExtractBoxWithPayload. The structural walk in boxes.go
// already proved nesting integrity; here we only read typed tables.
func extractTracks(source string, raw []byte, c *Container) ([]*Track, error) {
	r := bytes.NewReader(raw)
	trakInfos, err := mp4.ExtractBox(r, nil, mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(),
	})
	if err != nil {
		return nil, errs.New(errs.KindContainer, source, errs.Unknown,
			errs.Unknown, 0, "trak extraction failed: %v", err)
	}
	if len(trakInfos) == 0 {
		return nil, errs.New(errs.KindTrack, source, errs.Unknown, errs.Unknown, 0,
			"moov contains no trak")
	}
	tracks := make([]*Track, 0, len(trakInfos))
	for _, info := range trakInfos {
		tk, err := readTrack(source, r, info, int64(len(raw)))
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, tk)
	}
	return tracks, nil
}

// extractOne pulls a single optional leaf box's typed payload from a trak.
func extractOne(r *bytes.Reader, trak *mp4.BoxInfo, path ...mp4.BoxType) (mp4.IBox, bool, error) {
	full := append(mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak()}, path...)
	got, err := mp4.ExtractBoxWithPayload(r, trak, full[2:])
	if err != nil {
		return nil, false, err
	}
	if len(got) == 0 {
		return nil, false, nil
	}
	return got[0].Payload, true, nil
}

func readTrack(source string, r *bytes.Reader, trak *mp4.BoxInfo, fileSize int64) (*Track, error) {
	tk := &Track{}

	tkhdBox, ok, err := extractOne(r, trak, mp4.BoxTypeTkhd())
	if err != nil {
		return nil, wrapBoxErr(source, trak, err)
	}
	if !ok {
		return nil, errs.New(errs.KindTrack, source, int64(trak.Offset),
			errs.Unknown, 0, "trak missing tkhd")
	}
	tk.TrackID = tkhdBox.(*mp4.Tkhd).TrackID

	mdhdBox, ok, err := extractOne(r, trak, mp4.BoxTypeMdia(), mp4.BoxTypeMdhd())
	if err != nil {
		return nil, wrapBoxErr(source, trak, err)
	}
	if !ok {
		return nil, errs.New(errs.KindTrack, source, int64(trak.Offset),
			errs.Unknown, tk.TrackID, "trak missing mdhd")
	}
	mdhd := mdhdBox.(*mp4.Mdhd)
	tk.Timescale = mdhd.Timescale
	tk.Duration = mdhdDuration(mdhd)

	if hdlrBox, found, _ := extractOne(r, trak,
		mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()); found && hdlrBox != nil {
		tk.Handler = string(hdlrBox.(*mp4.Hdlr).HandlerType[:])
	}

	stblPath := func(leaf mp4.BoxType) []mp4.BoxType {
		return []mp4.BoxType{
			mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), leaf,
		}
	}

	stszBox, ok, err := extractOne(r, trak, stblPath(mp4.BoxTypeStsz())...)
	if err != nil {
		return nil, wrapBoxErr(source, trak, err)
	}
	if !ok {
		return nil, errs.New(errs.KindTrack, source, int64(trak.Offset),
			errs.Unknown, tk.TrackID, "trak missing stsz")
	}
	stsz := stszBox.(*mp4.Stsz)
	tk.Samples = stsz.SampleCount

	// stss is optional; absence means every sample is a sync sample.
	stssBox, found, err := extractOne(r, trak, stblPath(mp4.BoxTypeStss())...)
	if err != nil {
		return nil, wrapBoxErr(source, trak, err)
	}
	if found && stssBox != nil {
		tk.Keyframes = append(tk.Keyframes, stssBox.(*mp4.Stss).SampleNumber...)
	} else {
		for i := uint32(1); i <= stsz.SampleCount; i++ {
			tk.Keyframes = append(tk.Keyframes, i)
		}
	}

	stscBox, found, err := extractOne(r, trak, stblPath(mp4.BoxTypeStsc())...)
	if err != nil {
		return nil, wrapBoxErr(source, trak, err)
	}
	var stscEntries []mp4.StscEntry
	if found {
		stscEntries = stscBox.(*mp4.Stsc).Entries
	}

	var chunkOffsets []uint64
	if stcoBox, found, _ := extractOne(r, trak, stblPath(mp4.BoxTypeStco())...); found && stcoBox != nil {
		for _, o := range stcoBox.(*mp4.Stco).ChunkOffset {
			chunkOffsets = append(chunkOffsets, uint64(o))
		}
	} else if co64Box, found64, e := extractOne(r, trak, stblPath(mp4.BoxTypeCo64())...); e != nil {
		return nil, wrapBoxErr(source, trak, e)
	} else if found64 {
		chunkOffsets = append(chunkOffsets, co64Box.(*mp4.Co64).ChunkOffset...)
	}

	var sttsEntries []mp4.SttsEntry
	if sttsBox, found, _ := extractOne(r, trak, stblPath(mp4.BoxTypeStts())...); found && sttsBox != nil {
		sttsEntries = sttsBox.(*mp4.Stts).Entries
	}
	tk.buildSampleSpans(stsz, chunkOffsets, stscEntries, sttsEntries)

	if err := tk.validate(source, fileSize); err != nil {
		return nil, err
	}
	return tk, nil
}

func wrapBoxErr(source string, trak *mp4.BoxInfo, err error) error {
	return errs.New(errs.KindContainer, source, int64(trak.Offset),
		errs.Unknown, 0, "sample table read failed: %v", err)
}

func mdhdDuration(m *mp4.Mdhd) int64 {
	if m.Version == 1 {
		return int64(m.DurationV1)
	}
	return int64(m.DurationV0)
}

// buildSampleSpans expands stsc/stco/stsz into explicit per-sample byte
// spans. Anything inconsistent is left empty and caught by validate().
func (tk *Track) buildSampleSpans(stsz *mp4.Stsz, stco []uint64, stsc []mp4.StscEntry, stts []mp4.SttsEntry) {
	_ = stts
	sizes := sampleSizes(stsz)
	if len(sizes) != int(stsz.SampleCount) {
		return
	}
	perChunk, ok := expandStsc(stsc, len(stco))
	if !ok {
		return
	}
	total := 0
	for _, n := range perChunk {
		total += n
	}
	if total != len(sizes) || len(stco) != len(perChunk) {
		return
	}
	sampleIdx := 0
	for chunk, off := range stco {
		cursor := int64(off)
		for j := 0; j < perChunk[chunk]; j++ {
			length := int64(sizes[sampleIdx])
			tk.SampleRanges = append(tk.SampleRanges,
				ByteSpan{Offset: cursor, Length: length})
			tk.ChunkRanges = append(tk.ChunkRanges,
				ByteSpan{Offset: cursor, Length: length})
			cursor += length
			sampleIdx++
		}
	}
}

func sampleSizes(stsz *mp4.Stsz) []int64 {
	if stsz.SampleSize != 0 {
		out := make([]int64, stsz.SampleCount)
		for i := range out {
			out[i] = int64(stsz.SampleSize)
		}
		return out
	}
	out := make([]int64, 0, len(stsz.EntrySize))
	for _, s := range stsz.EntrySize {
		out = append(out, int64(s))
	}
	return out
}

// expandStsc converts first-chunk entries into a samples-per-chunk list.
func expandStsc(entries []mp4.StscEntry, chunkCount int) ([]int, bool) {
	if len(entries) == 0 || chunkCount == 0 {
		return nil, false
	}
	out := make([]int, chunkCount)
	for i, e := range entries {
		first := int(e.FirstChunk) - 1 // stsc first_chunk is 1-based
		if first < 0 || first >= chunkCount {
			return nil, false
		}
		last := chunkCount - 1
		if i+1 < len(entries) {
			last = int(entries[i+1].FirstChunk) - 2
		}
		for c := first; c <= last; c++ {
			out[c] = int(e.SamplesPerChunk)
		}
	}
	for _, n := range out {
		if n <= 0 {
			return nil, false
		}
	}
	return out, true
}

func (tk *Track) validate(source string, fileSize int64) error {
	if !clock.ValidScale(tk.Timescale) {
		return errs.New(errs.KindTrack, source, errs.Unknown, errs.Unknown, tk.TrackID,
			"track %d has invalid timescale %d", tk.TrackID, tk.Timescale)
	}
	if tk.Duration <= 0 {
		return errs.New(errs.KindTrack, source, errs.Unknown, errs.Unknown, tk.TrackID,
			"track %d declares non-positive duration %d", tk.TrackID, tk.Duration)
	}
	if tk.Samples == 0 {
		return errs.New(errs.KindTrack, source, errs.Unknown, errs.Unknown, tk.TrackID,
			"track %d has no samples", tk.TrackID)
	}
	if len(tk.SampleRanges) != int(tk.Samples) {
		return errs.New(errs.KindTrack, source, errs.Unknown, errs.Unknown, tk.TrackID,
			"track %d sample table inconsistent: %d spans vs %d samples",
			tk.TrackID, len(tk.SampleRanges), tk.Samples)
	}
	for i, span := range tk.SampleRanges {
		if span.Offset < 0 || span.Length <= 0 || span.End() > fileSize {
			return errs.New(errs.KindTrack, source, span.Offset, errs.Unknown, tk.TrackID,
				"track %d sample %d byte span [%d,%d) out of file range [0,%d)",
				tk.TrackID, i, span.Offset, span.End(), fileSize)
		}
	}
	for _, n := range tk.Keyframes {
		if n < 1 || n > tk.Samples {
			return errs.New(errs.KindTrack, source, errs.Unknown, errs.Unknown, tk.TrackID,
				"track %d keyframe sample %d outside [1,%d]", tk.TrackID, n, tk.Samples)
		}
	}
	return nil
}

// String renders a compact track summary for terminal output.
func (tk *Track) String() string {
	return fmt.Sprintf("track(id=%d handler=%q timescale=%d duration=%d samples=%d keyframes=%d)",
		tk.TrackID, tk.Handler, tk.Timescale, tk.Duration, tk.Samples, len(tk.Keyframes))
}
