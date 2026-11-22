// Package container parses locally generated MP4/MOV files into samples with
// exact DTS/PTS expressed both in the track timescale and in nanoseconds.
package container

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/abema/go-mp4"

	"mediapipeline/internal/media"
)

// Sample is one media sample. Original values are preserved verbatim so that
// provenance and replay can be checked against the source container.
type Sample struct {
	Index      int    // 0-based index in decode order
	Size       uint32 // sample size in bytes
	Offset     uint64 // absolute byte offset of the sample payload in the file
	DTS        int64  // decode timestamp in track timescale ticks
	PTS        int64  // presentation ("composition") timestamp in track ticks
	Delta      uint32 // decode duration in track ticks
	Keyframe   bool
	DTSNanos   media.Nano
	PTSNanos   media.Nano
	DeltaNanos media.Nano
}

// Track is one parsed track with its sample table resolved.
type Track struct {
	ID        uint32
	Kind      string // "video", "audio" or "other"
	Timescale uint32
	Duration  uint64 // mdhd duration in track ticks
	Samples   []Sample
	Keyframes []int // sample indexes (decode order) flagged as sync samples
}

// Movie is the parsed container.
type Movie struct {
	Path           string
	MajorBrand     string
	Compatible     []string
	MovieTimescale uint32
	MovieDuration  uint64
	Tracks         []*Track
}

const defaultMaxSamples = 5_000_000

// Parser validates and resolves a file.
type Parser struct {
	// MaxSamples bounds memory use; zero means 5_000_000 samples per track.
	MaxSamples uint32
}

// ParseFile opens path and parses it.
func ParseFile(path string) (*Movie, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, media.WrapError(media.InvalidInput, err, "opening %s", path)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, media.WrapError(media.InvalidInput, err, "stat %s", path)
	}
	movie, err := (&Parser{}).Parse(f, st.Size())
	if err != nil {
		return nil, err
	}
	movie.Path = path
	return movie, nil
}

// Parse parses the reader whose total length is size. Truncation is detected
// by comparing declared box extents with the real file size.
func Parse(r io.ReadSeeker, size int64) (*Movie, error) {
	return (&Parser{}).Parse(r, size)
}

// Parse parses with explicit limits.
func (p *Parser) Parse(r io.ReadSeeker, size int64) (*Movie, error) {
	if size <= 0 {
		return nil, media.NewError(media.TruncatedContainer, "file is empty")
	}
	movie := &Movie{}
	var moov *mp4.BoxInfo
	off := int64(0)
	for off < size {
		if size-off < 8 {
			return nil, media.NewError(media.TruncatedContainer,
				"trailing %d bytes cannot hold a box header", size-off).AtOffset(off)
		}
		if _, err := r.Seek(off, io.SeekStart); err != nil {
			return nil, media.WrapError(media.InvalidInput, err, "seek to %d", off)
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return nil, media.WrapError(media.TruncatedContainer, err,
				"reading box header").AtOffset(off)
		}
		if bi.Size < bi.HeaderSize {
			return nil, media.NewError(media.TruncatedContainer,
				"box %s declares size %d smaller than header %d",
				bi.Type.String(), bi.Size, bi.HeaderSize).AtOffset(off)
		}
		if off+int64(bi.Size) > size {
			return nil, media.NewError(media.TruncatedContainer,
				"box %s declares %d bytes but only %d remain",
				bi.Type.String(), bi.Size, size-off).AtOffset(off)
		}
		switch bi.Type {
		case mp4.BoxTypeFtyp():
			if err := readFtyp(r, movie, bi); err != nil {
				return nil, err
			}
		case mp4.BoxTypeMoov():
			cp := *bi
			moov = &cp
		}
		off += int64(bi.Size)
	}
	if moov == nil {
		return nil, media.NewError(media.CorruptSampleTable,
			"no moov box: cannot resolve sample tables").AtOffset(0)
	}
	if err := p.readMoov(r, movie, moov); err != nil {
		return nil, err
	}
	if len(movie.Tracks) == 0 {
		return nil, media.NewError(media.CorruptSampleTable, "moov contains no tracks")
	}
	for _, tr := range movie.Tracks {
		if err := validateSamples(tr, size); err != nil {
			return nil, err
		}
	}
	return movie, nil
}

func readFtyp(r io.ReadSeeker, movie *Movie, bi *mp4.BoxInfo) error {
	if _, err := bi.SeekToPayload(r); err != nil {
		return media.WrapError(media.TruncatedContainer, err, "seek to ftyp").AtOffset(int64(bi.Offset))
	}
	var ftyp mp4.Ftyp
	if _, err := mp4.Unmarshal(r, bi.Size-bi.HeaderSize, &ftyp, mp4.Context{}); err != nil {
		return media.WrapError(media.TruncatedContainer, err, "parsing ftyp").AtOffset(int64(bi.Offset))
	}
	movie.MajorBrand = string(ftyp.MajorBrand[:])
	for _, cb := range ftyp.CompatibleBrands {
		movie.Compatible = append(movie.Compatible, string(cb.CompatibleBrand[:]))
	}
	return nil
}

func (p *Parser) readMoov(r io.ReadSeeker, movie *Movie, moov *mp4.BoxInfo) error {
	if _, err := moov.SeekToStart(r); err != nil {
		return media.WrapError(media.TruncatedContainer, err, "seek to moov").AtOffset(int64(moov.Offset))
	}
	mvhd, err := mp4.ExtractBoxesWithPayload(r, moov, []mp4.BoxPath{{mp4.BoxTypeMvhd()}})
	if err != nil {
		return media.WrapError(media.CorruptSampleTable, err, "reading mvhd").AtOffset(int64(moov.Offset))
	}
	for _, bip := range mvhd {
		if b, ok := bip.Payload.(*mp4.Mvhd); ok {
			movie.MovieTimescale = b.Timescale
			movie.MovieDuration = b.GetDuration()
		}
	}
	traks, err := mp4.ExtractBoxes(r, moov, []mp4.BoxPath{{mp4.BoxTypeTrak()}})
	if err != nil {
		return media.WrapError(media.CorruptSampleTable, err, "reading trak boxes").AtOffset(int64(moov.Offset))
	}
	for _, trak := range traks {
		tr, err := p.readTrak(r, trak)
		if err != nil {
			return err
		}
		movie.Tracks = append(movie.Tracks, tr)
	}
	return nil
}

type trakBoxes struct {
	tkhd, mdhd, hdlr, stsd *mp4.BoxInfo
	stts, ctts, stss, stsc *mp4.BoxInfo
	stsz, stco, co64       *mp4.BoxInfo
}

func (p *Parser) readTrak(r io.ReadSeeker, trak *mp4.BoxInfo) (*Track, error) {
	// Peek at tkhd first so every later diagnostic can carry the track id.
	tkhdPeek, err := mp4.ExtractBoxesWithPayload(r, trak, []mp4.BoxPath{{mp4.BoxTypeTkhd()}})
	if err != nil {
		return nil, media.WrapError(media.CorruptSampleTable, err,
			"reading tkhd").AtOffset(int64(trak.Offset))
	}
	peekID := int32(0)
	if len(tkhdPeek) > 0 {
		if t, ok := tkhdPeek[0].Payload.(*mp4.Tkhd); ok {
			peekID = int32(t.TrackID)
		}
	}
	paths := []mp4.BoxPath{
		{mp4.BoxTypeTkhd()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStts()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCtts()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsc()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsz()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStco()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCo64()},
	}
	bips, err := mp4.ExtractBoxesWithPayload(r, trak, paths)
	if err != nil {
		return nil, media.WrapError(media.CorruptSampleTable, err,
			"walking track boxes").AtTrack(peekID).AtOffset(int64(trak.Offset))
	}
	var (
		tkhd *mp4.Tkhd
		mdhd *mp4.Mdhd
		hdlr *mp4.Hdlr
		stsd *mp4.Stsd
		stts *mp4.Stts
		ctts *mp4.Ctts
		stss *mp4.Stss
		stsc *mp4.Stsc
		stsz *mp4.Stsz
		stco *mp4.Stco
		co64 *mp4.Co64
	)
	for _, bip := range bips {
		switch box := bip.Payload.(type) {
		case *mp4.Tkhd:
			tkhd = box
		case *mp4.Mdhd:
			mdhd = box
		case *mp4.Hdlr:
			hdlr = box
		case *mp4.Stsd:
			stsd = box
		case *mp4.Stts:
			stts = box
		case *mp4.Ctts:
			ctts = box
		case *mp4.Stss:
			stss = box
		case *mp4.Stsc:
			if stsc != nil {
				return nil, media.NewError(media.CorruptSampleTable,
					"track has more than one stsc box").AtTrack(int32(tkhd.TrackID)).AtOffset(int64(trak.Offset))
			}
			stsc = box
		case *mp4.Stsz:
			stsz = box
		case *mp4.Stco:
			if stco != nil {
				return nil, media.NewError(media.CorruptSampleTable,
					"track has more than one stco box").AtTrack(int32(tkhd.TrackID)).AtOffset(int64(trak.Offset))
			}
			stco = box
		case *mp4.Co64:
			co64 = box
		}
	}
	if tkhd == nil || mdhd == nil {
		return nil, media.NewError(media.CorruptSampleTable,
			"track missing tkhd or mdhd").AtOffset(int64(trak.Offset))
	}
	trackID := int32(tkhd.TrackID)
	if mdhd.Timescale == 0 {
		return nil, media.NewError(media.ZeroTimescale,
			"mdhd timescale is zero").AtTrack(trackID).AtOffset(int64(trak.Offset))
	}
	if stts == nil || stsc == nil || stsz == nil || (stco == nil && co64 == nil) {
		return nil, media.NewError(media.CorruptSampleTable,
			"track is missing one of stts/stsc/stsz/stco").AtTrack(trackID).AtOffset(int64(trak.Offset))
	}
	if stsd == nil || stsd.EntryCount == 0 {
		return nil, media.NewError(media.CorruptSampleTable,
			"track has no sample description").AtTrack(trackID).AtOffset(int64(trak.Offset))
	}

	kind := "other"
	if hdlr != nil {
		switch string(hdlr.HandlerType[:]) {
		case "vide":
			kind = "video"
		case "soun":
			kind = "audio"
		}
	}
	tr := &Track{ID: tkhd.TrackID, Kind: kind, Timescale: mdhd.Timescale, Duration: mdhd.GetDuration()}
	samples, err := p.resolveSamples(tr, stts, ctts, stss, stsc, stsz, stco, co64)
	if err != nil {
		return nil, err
	}
	tr.Samples = samples
	for i := range samples {
		if samples[i].Keyframe {
			tr.Keyframes = append(tr.Keyframes, i)
		}
	}
	return tr, nil
}

func (p *Parser) resolveSamples(tr *Track, stts *mp4.Stts, ctts *mp4.Ctts, stss *mp4.Stss,
	stsc *mp4.Stsc, stsz *mp4.Stsz, stco *mp4.Stco, co64 *mp4.Co64) ([]Sample, error) {
	trackID := int32(tr.ID)
	tb, err := media.NewTimebase(tr.Timescale)
	if err != nil {
		return nil, media.AsError(err).AtTrack(trackID)
	}
	maxSamples := p.MaxSamples
	if maxSamples == 0 {
		maxSamples = defaultMaxSamples
	}
	if stsz.SampleCount == 0 {
		return nil, media.NewError(media.CorruptSampleTable, "sample count is zero").AtTrack(trackID)
	}
	if stsz.SampleCount > maxSamples {
		return nil, media.NewError(media.InvalidInput,
			"sample count %d exceeds limit %d", stsz.SampleCount, maxSamples).AtTrack(trackID)
	}
	count := int(stsz.SampleCount)

	deltas, err := expandStts(stts, count, trackID)
	if err != nil {
		return nil, err
	}
	offsetsInTime, err := expandCtts(ctts, count, trackID)
	if err != nil {
		return nil, err
	}
	sizes, err := sampleSizes(stsz, count, trackID)
	if err != nil {
		return nil, err
	}
	fileOffsets, err := sampleOffsets(stco, co64, stsc, sizes, count, trackID)
	if err != nil {
		return nil, err
	}

	samples := make([]Sample, count)
	var dts int64
	for i := 0; i < count; i++ {
		dtsNanos, err := tb.Nanos(dts)
		if err != nil {
			return nil, err.(*media.Error).AtTrack(trackID)
		}
		deltaNanos, err := tb.Duration(int64(deltas[i]))
		if err != nil {
			return nil, err.(*media.Error).AtTrack(trackID)
		}
		ct := offsetsInTime[i]
		if (ct > 0 && dts > (1<<62)-ct) || (ct < 0 && dts < -(1<<62)-ct) {
			return nil, media.NewError(media.Overflow,
				"PTS computation overflows at sample %d", i).AtTrack(trackID)
		}
		pts := dts + ct
		ptsNanos, err := tb.Nanos(pts)
		if err != nil {
			return nil, err.(*media.Error).AtTrack(trackID)
		}
		samples[i] = Sample{
			Index:      i,
			Size:       sizes[i],
			Offset:     fileOffsets[i],
			DTS:        dts,
			PTS:        pts,
			Delta:      deltas[i],
			Keyframe:   isKeyframe(stss, i),
			DTSNanos:   dtsNanos,
			PTSNanos:   ptsNanos,
			DeltaNanos: deltaNanos,
		}
		dts += int64(deltas[i])
	}
	return samples, nil
}

// isKeyframe applies the ISO rule that a track without stss is all-sync.
func isKeyframe(stss *mp4.Stss, index int) bool {
	if stss == nil {
		return true
	}
	for _, n := range stss.SampleNumber {
		if int(n) == index+1 {
			return true
		}
	}
	return false
}

func expandStts(stts *mp4.Stts, count int, trackID int32) ([]uint32, error) {
	if int(stts.EntryCount) != len(stts.Entries) {
		return nil, media.NewError(media.CorruptSampleTable,
			"stts entry_count %d does not match %d entries", stts.EntryCount, len(stts.Entries)).AtTrack(trackID)
	}
	deltas := make([]uint32, 0, count)
	var total float64
	for ei, e := range stts.Entries {
		if uint64(e.SampleCount) > uint64(count-len(deltas)) {
			return nil, media.NewError(media.CorruptSampleTable,
				"stts run %d of %d samples overruns sample count %d", ei, e.SampleCount, count).AtTrack(trackID)
		}
		for i := uint32(0); i < e.SampleCount; i++ {
			deltas = append(deltas, e.SampleDelta)
		}
		total += float64(e.SampleCount) * float64(e.SampleDelta)
		if total > 1<<62 {
			return nil, media.NewError(media.Overflow,
				"cumulative decode duration overflows int64").AtTrack(trackID)
		}
	}
	if len(deltas) != count {
		return nil, media.NewError(media.CorruptSampleTable,
			"stts covers %d samples but stsz declares %d", len(deltas), count).AtTrack(trackID)
	}
	return deltas, nil
}

func expandCtts(ctts *mp4.Ctts, count int, trackID int32) ([]int64, error) {
	offsets := make([]int64, 0, count)
	if ctts == nil {
		for len(offsets) < count {
			offsets = append(offsets, 0)
		}
		return offsets, nil
	}
	if int(ctts.EntryCount) != len(ctts.Entries) {
		return nil, media.NewError(media.CorruptSampleTable,
			"ctts entry_count %d does not match %d entries", ctts.EntryCount, len(ctts.Entries)).AtTrack(trackID)
	}
	for ei, e := range ctts.Entries {
		if uint64(e.SampleCount) > uint64(count-len(offsets)) {
			return nil, media.NewError(media.CorruptSampleTable,
				"ctts run %d of %d samples overruns sample count %d", ei, e.SampleCount, count).AtTrack(trackID)
		}
		off := ctts.GetSampleOffset(ei)
		for i := uint32(0); i < e.SampleCount; i++ {
			offsets = append(offsets, off)
		}
	}
	if len(offsets) != count {
		return nil, media.NewError(media.CorruptSampleTable,
			"ctts covers %d samples but stsz declares %d", len(offsets), count).AtTrack(trackID)
	}
	return offsets, nil
}

func sampleSizes(stsz *mp4.Stsz, count int, trackID int32) ([]uint32, error) {
	sizes := make([]uint32, count)
	if stsz.SampleSize != 0 {
		for i := range sizes {
			sizes[i] = stsz.SampleSize
		}
		return sizes, nil
	}
	if len(stsz.EntrySize) < count {
		return nil, media.NewError(media.CorruptSampleTable,
			"stsz has %d entry sizes for %d samples", len(stsz.EntrySize), count).AtTrack(trackID)
	}
	copy(sizes, stsz.EntrySize[:count])
	for i, s := range sizes {
		if s == 0 {
			return nil, media.NewError(media.CorruptSampleTable,
				"sample %d has zero size", i).AtTrack(trackID)
		}
	}
	return sizes, nil
}

// sampleOffsets maps every sample to its absolute file offset using stco/co64
// chunk starts and the stsc run-length table.
func sampleOffsets(stco *mp4.Stco, co64 *mp4.Co64, stsc *mp4.Stsc, sizes []uint32, count int, trackID int32) ([]uint64, error) {
	var chunks []uint64
	switch {
	case stco != nil:
		if int(stco.EntryCount) != len(stco.ChunkOffset) {
			return nil, media.NewError(media.CorruptSampleTable,
				"stco entry_count %d does not match %d offsets", stco.EntryCount, len(stco.ChunkOffset)).AtTrack(trackID)
		}
		for _, o := range stco.ChunkOffset {
			chunks = append(chunks, uint64(o))
		}
	case co64 != nil:
		if int(co64.EntryCount) != len(co64.ChunkOffset) {
			return nil, media.NewError(media.CorruptSampleTable,
				"co64 entry_count %d does not match %d offsets", co64.EntryCount, len(co64.ChunkOffset)).AtTrack(trackID)
		}
		chunks = append(chunks, co64.ChunkOffset...)
	}
	if len(chunks) == 0 {
		return nil, media.NewError(media.CorruptSampleTable, "track has no chunk offsets").AtTrack(trackID)
	}
	if int(stsc.EntryCount) != len(stsc.Entries) {
		return nil, media.NewError(media.CorruptSampleTable,
			"stsc entry_count %d does not match %d entries", stsc.EntryCount, len(stsc.Entries)).AtTrack(trackID)
	}
	if len(stsc.Entries) == 0 {
		return nil, media.NewError(media.CorruptSampleTable, "stsc has no entries").AtTrack(trackID)
	}
	if stsc.Entries[0].FirstChunk != 1 {
		return nil, media.NewError(media.CorruptSampleTable,
			"stsc first entry must start at chunk 1, got %d", stsc.Entries[0].FirstChunk).AtTrack(trackID)
	}

	// runForChunk returns the stsc entry governing a 1-based chunk number.
	runForChunk := func(chunkNo uint32) *mp4.StscEntry {
		var chosen *mp4.StscEntry
		for i := range stsc.Entries {
			if stsc.Entries[i].FirstChunk <= chunkNo {
				chosen = &stsc.Entries[i]
			} else {
				break
			}
		}
		return chosen
	}

	out := make([]uint64, 0, count)
	for ci, chunkStart := range chunks {
		chunkNo := uint32(ci + 1)
		entry := runForChunk(chunkNo)
		if entry == nil {
			return nil, media.NewError(media.CorruptSampleTable,
				"no stsc entry covers chunk %d", chunkNo).AtTrack(trackID)
		}
		if entry.SamplesPerChunk == 0 {
			return nil, media.NewError(media.CorruptSampleTable,
				"stsc gives chunk %d zero samples", chunkNo).AtTrack(trackID)
		}
		cursor := chunkStart
		for s := uint32(0); s < entry.SamplesPerChunk; s++ {
			if len(out) >= count {
				return nil, media.NewError(media.CorruptSampleTable,
					"stsc assigns more than %d samples", count).AtTrack(trackID)
			}
			out = append(out, cursor)
			next := cursor + uint64(sizes[len(out)-1])
			if next < cursor {
				return nil, media.NewError(media.Overflow,
					"chunk offset arithmetic overflows").AtTrack(trackID)
			}
			cursor = next
		}
	}
	if len(out) != count {
		return nil, media.NewError(media.CorruptSampleTable,
			"chunk tables cover %d samples but stsz declares %d", len(out), count).AtTrack(trackID)
	}
	return out, nil
}

func validateSamples(tr *Track, size int64) error {
	trackID := int32(tr.ID)
	if len(tr.Samples) == 0 {
		return media.NewError(media.CorruptSampleTable,
			"track has zero samples").AtTrack(trackID)
	}
	var prevDTS int64
	var prevEnd uint64
	for i := range tr.Samples {
		s := &tr.Samples[i]
		if i > 0 && s.DTS < prevDTS {
			return media.NewError(media.OutOfOrderTimestamp,
				"sample %d DTS %d is before previous DTS %d", i, s.DTS, prevDTS).AtTrack(trackID)
		}
		prevDTS = s.DTS
		if i > 0 && s.Offset < prevEnd {
			return media.NewError(media.CorruptSampleTable,
				"sample %d starts at %d, overlapping previous sample ending at %d",
				i, s.Offset, prevEnd).AtTrack(trackID).AtOffset(int64(s.Offset))
		}
		end := s.Offset + uint64(s.Size)
		if end < s.Offset {
			return media.NewError(media.Overflow,
				"sample %d offset+size overflows uint64", i).AtTrack(trackID)
		}
		if end > uint64(size) {
			return media.NewError(media.TruncatedContainer,
				"sample %d spans [%d,%d) beyond file size %d", i, s.Offset, end, size).
				AtTrack(trackID).AtOffset(int64(s.Offset))
		}
		prevEnd = end
	}
	return nil
}

// KeyframeTimes returns keyframe presentation timestamps in ascending order.
func (tr *Track) KeyframeTimes() []media.Nano {
	out := make([]media.Nano, 0, len(tr.Keyframes))
	for _, idx := range tr.Keyframes {
		out = append(out, tr.Samples[idx].PTSNanos)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// String renders a compact track summary for terminal output.
func (tr *Track) String() string {
	return fmt.Sprintf("track %d (%s) timescale=%d samples=%d keyframes=%d",
		tr.ID, tr.Kind, tr.Timescale, len(tr.Samples), len(tr.Keyframes))
}
