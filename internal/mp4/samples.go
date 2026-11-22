package mp4

import (
	"errors"
	"fmt"
	"math"
	"sort"

	isom "github.com/abema/go-mp4"

	"github.com/pairwise/mediad/internal/fault"
)

// Sample is one track sample with its placement in the file.
type Sample struct {
	Index    int
	DTS      uint64
	Duration uint32
	CTO      int64
	Offset   int64
	Size     uint32
	Sync     bool
	InMdat   bool
}

// CTS returns the composition timestamp, reporting overflow.
func (s Sample) CTS() (uint64, error) {
	v := int64(s.DTS) + s.CTO
	if v < 0 || s.DTS > math.MaxInt64 {
		return 0, fault.Errorf("mp4.cts", fault.KindTimeline, "negative composition time for sample %d", s.Index)
	}
	return uint64(v), nil
}

// End returns the exclusive byte end of the sample.
func (s Sample) End() int64 { return s.Offset + int64(s.Size) }

// EditSpec is an elst entry before conversion to the unified clock.
type EditSpec struct {
	SegmentTicks uint64 // movie timescale
	MediaTicks   int64  // media timescale, -1 for an empty edit
	RateFixed    int32
}

// Track is one parsed track.
type Track struct {
	ID               uint32
	Handler          string
	Entry            string
	Timescale        uint32
	MediaDuration    uint64
	SampleRate       uint32
	Channels         int
	Edits            []EditSpec
	Samples          []Sample
	IndexComplete    bool
	DroppedSamples   int
	SyncTablePresent bool
	// TimestampBits is the width of the media decode clock: 32 for mdhd
	// version 0, 64 for version 1. Legacy 32-bit clocks wrap at 2^32.
	TimestampBits int
	Warnings      []*fault.Error

	// Raw tables retained for reconstruction when IndexComplete is false.
	Sizes        []uint32
	ChunkOffsets []int64
	PerChunk     []uint32
	Stbl         map[string]*Box
}

// HasSamples reports whether the track produced any verified sample.
func (t *Track) HasSamples() bool { return len(t.Samples) > 0 }

// IsAudio reports whether the handler is sound.
func (t *Track) IsAudio() bool { return t.Handler == "soun" }

// MediaData is one readable mdat payload range.
type MediaData struct{ Start, End int64 }

// Movie is the parsed container.
type Movie struct {
	Path      string
	Timescale uint32
	Duration  uint64
	Tracks    []*Track
	Mdats     []MediaData
	Warnings  []*fault.Error
	Tree      *Tree
}

// ParseMovie parses mvhd and every trak. Damaged tracks yield partial sample
// tables and warnings; the returned error is non-nil only when nothing at all
// could be parsed, and is recoverable whenever any track survived.
func ParseMovie(tree *Tree) (*Movie, error) {
	m := &Movie{Path: tree.Path, Tree: tree, Warnings: append([]*fault.Error(nil), tree.Warnings...)}
	for _, b := range tree.Find("mdat") {
		if b.PayloadSize > 0 {
			m.Mdats = append(m.Mdats, MediaData{Start: b.PayloadOff, End: b.ReadableEnd()})
		}
	}
	mvhd := tree.First("mvhd")
	if mvhd == nil {
		return m, fault.Errorf("mp4.movie", fault.KindParse, "missing mvhd").WithPath(tree.Path).Fatal().Wrapped(fault.ErrIncomplete)
	}
	parsed, err := tree.Parse(mvhd)
	if err != nil {
		return m, fault.New("mp4.movie", fault.KindParse, err).WithPath(tree.Path).At(mvhd.Offset).Fatal()
	}
	mh, ok := parsed.(*isom.Mvhd)
	if !ok {
		return m, fault.Errorf("mp4.movie", fault.KindParse, "mvhd has unexpected type %T", parsed).WithPath(tree.Path).At(mvhd.Offset).Fatal()
	}
	m.Timescale = mh.Timescale
	m.Duration = mh.GetDuration()
	if m.Timescale == 0 {
		m.Warnings = append(m.Warnings, fault.Errorf("mp4.movie", fault.KindParse, "movie timescale is zero").
			WithPath(tree.Path).At(mvhd.Offset).Recovered())
		m.Timescale = 1000
	}

	seen := map[uint32]bool{}
	for _, trak := range tree.Find("trak") {
		tr, werrs := parseTrack(tree, trak)
		m.Warnings = append(m.Warnings, werrs...)
		if tr == nil {
			continue
		}
		validateAgainstMdat(m, tr, func(e *fault.Error) {
			e.Path = tree.Path
			e.TrackID = tr.ID
			m.Warnings = append(m.Warnings, e)
		})
		if seen[tr.ID] {
			m.Warnings = append(m.Warnings, fault.Errorf("mp4.track", fault.KindParse, "duplicate track id %d", tr.ID).
				WithPath(tree.Path).At(trak.Offset).WithTrack(tr.ID).Recovered())
			continue
		}
		seen[tr.ID] = true
		m.Tracks = append(m.Tracks, tr)
	}
	sort.Slice(m.Tracks, func(i, j int) bool { return m.Tracks[i].ID < m.Tracks[j].ID })

	var damaged []error
	for _, w := range m.Warnings {
		if w.Recoverable {
			damaged = append(damaged, w)
		}
	}
	if len(m.Tracks) == 0 {
		return m, fault.Errorf("mp4.movie", fault.KindParse, "no parseable tracks").WithPath(tree.Path).Fatal().Wrapped(fault.ErrIncomplete)
	}
	if len(damaged) > 0 {
		return m, fault.New("mp4.movie", fault.KindTruncated, errors.Join(damaged...)).WithPath(tree.Path).Recovered()
	}
	return m, nil
}

func parseTrack(tree *Tree, trak *Box) (*Track, []*fault.Error) {
	tr := &Track{IndexComplete: true, Stbl: map[string]*Box{}}
	var warns []*fault.Error
	warn := func(e *fault.Error) {
		if e != nil {
			e.Path = tree.Path
			e.TrackID = tr.ID
			warns = append(warns, e)
		}
	}
	for _, c := range trak.Children {
		if c.Type == "tkhd" {
			tr.ID = tree.trackIDFromTkhd(c)
		}
	}
	mdhd := firstDesc(trak, "mdhd")
	if mdhd == nil {
		warn(fault.Errorf("mp4.track", fault.KindParse, "missing mdhd").At(trak.Offset).Recovered().Wrapped(fault.ErrIncomplete))
		return nil, warns
	}
	if p, err := tree.Parse(mdhd); err != nil {
		warn(fault.AsRecovered(err, mdhd.Offset))
	} else if h, ok := p.(*isom.Mdhd); ok {
		tr.Timescale = h.Timescale
		tr.MediaDuration = h.GetDuration()
		tr.TimestampBits = 64
		if h.GetVersion() == 0 {
			tr.TimestampBits = 32
		}
	}
	if tr.Timescale == 0 {
		warn(fault.Errorf("mp4.track", fault.KindParse, "zero media timescale").At(mdhd.Offset).Recovered())
		return nil, warns
	}
	hdlr := firstDesc(trak, "hdlr")
	if hdlr != nil {
		if p, err := tree.Parse(hdlr); err != nil {
			warn(fault.AsRecovered(err, hdlr.Offset))
		} else if h, ok := p.(*isom.Hdlr); ok {
			tr.Handler = string(h.HandlerType[:])
		}
	}
	if elst := firstDesc(trak, "elst"); elst != nil {
		tr.Edits = parseElst(tree, elst, tr, warn)
	}
	stbl := firstDesc(trak, "stbl")
	if stbl == nil {
		warn(fault.Errorf("mp4.track", fault.KindParse, "missing stbl").At(trak.Offset).Recovered().Wrapped(fault.ErrIncomplete))
		tr.IndexComplete = false
		return tr, warns
	}
	for _, c := range stbl.Children {
		for _, typ := range []string{"stsd", "stts", "stsc", "stsz", "stco", "co64", "stss", "ctts"} {
			if c.Type == typ {
				tr.Stbl[typ] = c
			}
		}
	}
	parseSampleEntry(tree, tr, warn)
	durations := parseStts(tree, tr, warn)
	sizes := parseStsz(tree, tr, warn)
	cto := parseCtts(tree, tr, warn)
	chunks := parseChunkOffsets(tree, tr, warn)
	perChunk := parseStsc(tree, tr, warn, len(chunks))
	syncSet := parseStss(tree, tr, warn)

	count := len(sizes)
	if len(perChunk) == 0 && count > 0 {
		warn(fault.Errorf("mp4.track", fault.KindParse, "stsc has no usable entries").At(trak.Offset).Recovered().Wrapped(fault.ErrIncomplete))
		tr.IndexComplete = false
		return tr, warns
	}
	totalChunkSamples := 0
	for _, v := range perChunk {
		totalChunkSamples += int(v)
	}
	if totalChunkSamples != len(chunks) && len(chunks) > 0 {
		if totalChunkSamples < len(chunks) {
			// Extra chunk offsets carry no sample mapping metadata.
			tr.IndexComplete = false
		}
	}
	if count > totalChunkSamples && totalChunkSamples >= 0 && len(chunks) > 0 {
		tr.IndexComplete = false
	}
	sampleIdx := 0
	var dts uint64
	chunkIdx := 0
	for sampleIdx < count {
		if chunkIdx >= len(chunks) || chunkIdx >= len(perChunk) {
			tr.IndexComplete = false
			warn(fault.Errorf("mp4.track", fault.KindIndex, "chunk table covers %d of %d samples", sampleIdx, count).
				At(trak.Offset).Recovered().Wrapped(fault.ErrIncomplete))
			break
		}
		spc := int(perChunk[chunkIdx])
		if spc == 0 {
			tr.IndexComplete = false
			warn(fault.Errorf("mp4.track", fault.KindIndex, "stsc entry for chunk %d has zero samples", chunkIdx+1).
				At(trak.Offset).Recovered())
			break
		}
		off := chunks[chunkIdx]
		for j := 0; j < spc && sampleIdx < count; j++ {
			size := sizes[sampleIdx]
			s := Sample{Index: sampleIdx, DTS: dts, Offset: off, Size: size, CTO: ctoAt(cto, sampleIdx), Sync: syncSet[sampleIdx]}
			if sampleIdx < len(durations) {
				s.Duration = durations[sampleIdx]
			}
			tr.Samples = append(tr.Samples, s)
			if math.MaxUint64-dts < uint64(s.Duration) {
				warn(fault.Errorf("mp4.track", fault.KindTimeline, "dts overflow at sample %d", sampleIdx).
					At(off).Recovered().Wrapped(fault.ErrTimeOverflow))
				dts = math.MaxUint64
			} else {
				dts += uint64(s.Duration)
			}
			if tr.TimestampBits == 32 {
				// The container declares a 32-bit media clock; positions are
				// the low 32 bits of the accumulated duration, so a sum past
				// 2^32 wraps and the next stage reports a located wrap.
				s.DTS = dts & 0xFFFFFFFF
			}
			off += int64(size)
			sampleIdx++
		}
		chunkIdx++
	}
	return tr, warns
}

func asRecovered(err error, off int64) *fault.Error { return fault.AsRecovered(err, off) }

func ctoAt(table []int64, i int) int64 {
	if i < len(table) {
		return table[i]
	}
	return 0
}

func firstDesc(b *Box, typ string) *Box {
	if b == nil {
		return nil
	}
	if b.Type == typ {
		return b
	}
	for _, c := range b.Children {
		if found := firstDesc(c, typ); found != nil {
			return found
		}
	}
	return nil
}

func parseElst(tree *Tree, b *Box, tr *Track, warn func(*fault.Error)) []EditSpec {
	p, err := tree.Parse(b)
	if err != nil {
		warn(fault.AsRecovered(err, b.Offset))
		return nil
	}
	h, ok := p.(*isom.Elst)
	if !ok {
		warn(fault.Errorf("mp4.elst", fault.KindParse, "unexpected elst type %T", p).At(b.Offset).Recovered())
		return nil
	}
	out := make([]EditSpec, 0, len(h.Entries))
	for _, e := range h.Entries {
		es := EditSpec{RateFixed: int32(e.MediaRateInteger) << 16}
		if e.MediaRateInteger == 1 && e.MediaRateFraction == 0 {
			es.RateFixed = 0x00010000
		}
		switch elstVersion(tree, b) {
		case 1:
			es.SegmentTicks = e.SegmentDurationV1
			es.MediaTicks = e.MediaTimeV1
		default:
			es.SegmentTicks = uint64(e.SegmentDurationV0)
			es.MediaTicks = int64(e.MediaTimeV0)
		}
		out = append(out, es)
	}
	return out
}

func parseSampleEntry(tree *Tree, tr *Track, warn func(*fault.Error)) {
	stsd := tr.Stbl["stsd"]
	if stsd == nil || len(stsd.Children) == 0 {
		warn(fault.Errorf("mp4.stsd", fault.KindParse, "missing sample entry").At(stsdOffset(tr)).Recovered())
		return
	}
	entry := stsd.Children[0]
	tr.Entry = entry.Type
	buf, err := tree.ReadPayload(entry)
	if err != nil {
		warn(fault.AsRecovered(err, entry.Offset))
		return
	}
	// SampleEntry: 6 reserved + 2 data_reference_index.
	if len(buf) < 8 {
		warn(fault.Errorf("mp4.stsd", fault.KindTruncated, "sample entry shorter than 8 bytes").At(entry.Offset).Recovered())
		return
	}
	if tr.IsAudio() {
		if len(buf) < 28 {
			warn(fault.Errorf("mp4.stsd", fault.KindTruncated, "audio sample entry shorter than 28 bytes").At(entry.Offset).Recovered())
			return
		}
		tr.Channels = int(be16(buf[16:18]))
		sr := be32(buf[24:28])
		if sr>>16 != 0 {
			tr.SampleRate = sr >> 16
		} else {
			tr.SampleRate = sr
		}
	}
}

func stsdOffset(tr *Track) int64 {
	if b := tr.Stbl["stsd"]; b != nil {
		return b.Offset
	}
	return -1
}

func parseStts(tree *Tree, tr *Track, warn func(*fault.Error)) []uint32 {
	b := tr.Stbl["stts"]
	if b == nil {
		warn(fault.Errorf("mp4.stts", fault.KindParse, "missing stts").At(-1).Recovered().Wrapped(fault.ErrIncomplete))
		tr.IndexComplete = false
		return nil
	}
	p, err := tree.Parse(b)
	if err != nil {
		warn(fault.AsRecovered(err, b.Offset))
		tr.IndexComplete = false
		return nil
	}
	h := p.(*isom.Stts)
	var out []uint32
	for _, e := range h.Entries {
		if uint64(len(out))+uint64(e.SampleCount) > 1<<24 {
			warn(fault.Errorf("mp4.stts", fault.KindParse, "implausible sample count %d", e.SampleCount).At(b.Offset).Recovered())
			tr.IndexComplete = false
			break
		}
		for i := uint32(0); i < e.SampleCount; i++ {
			out = append(out, e.SampleDelta)
		}
	}
	return out
}

func parseStsz(tree *Tree, tr *Track, warn func(*fault.Error)) []uint32 {
	b := tr.Stbl["stsz"]
	if b == nil {
		warn(fault.Errorf("mp4.stsz", fault.KindParse, "missing stsz").At(-1).Recovered().Wrapped(fault.ErrIncomplete))
		tr.IndexComplete = false
		return nil
	}
	p, err := tree.Parse(b)
	if err != nil {
		warn(fault.AsRecovered(err, b.Offset))
		tr.IndexComplete = false
		return nil
	}
	h := p.(*isom.Stsz)
	count := int(h.SampleCount)
	if count < 0 || count > 1<<24 {
		warn(fault.Errorf("mp4.stsz", fault.KindParse, "implausible sample count %d", count).At(b.Offset).Recovered())
		tr.IndexComplete = false
		return nil
	}
	out := make([]uint32, count)
	if h.SampleSize != 0 {
		for i := range out {
			out[i] = h.SampleSize
		}
	} else {
		for i := 0; i < count && i < len(h.EntrySize); i++ {
			out[i] = h.EntrySize[i]
		}
		if len(h.EntrySize) < count {
			tr.IndexComplete = false
			warn(fault.Errorf("mp4.stsz", fault.KindTruncated, "stsz has %d of %d entries", len(h.EntrySize), count).
				At(b.Offset).Recovered().Wrapped(fault.ErrIncomplete))
		}
	}
	tr.Sizes = out
	return out
}

func parseCtts(tree *Tree, tr *Track, warn func(*fault.Error)) []int64 {
	b := tr.Stbl["ctts"]
	if b == nil {
		return nil
	}
	p, err := tree.Parse(b)
	if err != nil {
		warn(fault.AsRecovered(err, b.Offset))
		return nil
	}
	h := p.(*isom.Ctts)
	var out []int64
	for i, e := range h.Entries {
		for j := uint32(0); j < e.SampleCount; j++ {
			out = append(out, h.GetSampleOffset(i))
		}
	}
	return out
}

func parseChunkOffsets(tree *Tree, tr *Track, warn func(*fault.Error)) []int64 {
	if b := tr.Stbl["co64"]; b != nil {
		p, err := tree.Parse(b)
		if err != nil {
			warn(fault.AsRecovered(err, b.Offset))
			tr.IndexComplete = false
			return nil
		}
		h := p.(*isom.Co64)
		out := make([]int64, 0, len(h.ChunkOffset))
		for _, v := range h.ChunkOffset {
			if v > math.MaxInt64 {
				warn(fault.Errorf("mp4.co64", fault.KindParse, "chunk offset overflows int64").At(b.Offset).Recovered())
				tr.IndexComplete = false
				continue
			}
			out = append(out, int64(v))
		}
		tr.ChunkOffsets = out
		return out
	}
	b := tr.Stbl["stco"]
	if b == nil {
		warn(fault.Errorf("mp4.stco", fault.KindParse, "missing stco/co64").At(-1).Recovered().Wrapped(fault.ErrIncomplete))
		tr.IndexComplete = false
		return nil
	}
	p, err := tree.Parse(b)
	if err != nil {
		warn(fault.AsRecovered(err, b.Offset))
		tr.IndexComplete = false
		return nil
	}
	h := p.(*isom.Stco)
	out := make([]int64, 0, len(h.ChunkOffset))
	for _, v := range h.ChunkOffset {
		out = append(out, int64(v))
	}
	tr.ChunkOffsets = out
	return out
}

func parseStsc(tree *Tree, tr *Track, warn func(*fault.Error), chunkCount int) []uint32 {
	b := tr.Stbl["stsc"]
	if b == nil {
		warn(fault.Errorf("mp4.stsc", fault.KindParse, "missing stsc").At(-1).Recovered().Wrapped(fault.ErrIncomplete))
		tr.IndexComplete = false
		return nil
	}
	p, err := tree.Parse(b)
	if err != nil {
		warn(fault.AsRecovered(err, b.Offset))
		tr.IndexComplete = false
		return nil
	}
	h := p.(*isom.Stsc)
	if len(h.Entries) == 0 {
		warn(fault.Errorf("mp4.stsc", fault.KindParse, "stsc has no entries").At(b.Offset).Recovered().Wrapped(fault.ErrIncomplete))
		tr.IndexComplete = false
		return nil
	}
	// Expand run-length entries onto one slot per chunk (1-based FirstChunk).
	out := make([]uint32, 0, chunkCount)
	ei := 0
	for chunk := 1; chunk <= chunkCount; chunk++ {
		for ei+1 < len(h.Entries) && int(h.Entries[ei+1].FirstChunk) <= chunk {
			ei++
		}
		e := h.Entries[ei]
		if e.FirstChunk != 1 && chunk == 1 {
			warn(fault.Errorf("mp4.stsc", fault.KindParse, "first stsc entry starts at chunk %d, not 1", e.FirstChunk).
				At(b.Offset).Recovered().Wrapped(fault.ErrIncomplete))
			tr.IndexComplete = false
			return nil
		}
		if e.SamplesPerChunk > 4096 {
			warn(fault.Errorf("mp4.stsc", fault.KindParse, "implausible samples-per-chunk %d", e.SamplesPerChunk).
				At(b.Offset).Recovered())
			tr.IndexComplete = false
			return nil
		}
		out = append(out, e.SamplesPerChunk)
	}
	tr.PerChunk = out
	return out
}

func parseStss(tree *Tree, tr *Track, warn func(*fault.Error)) map[int]bool {
	b := tr.Stbl["stss"]
	if b == nil {
		return nil
	}
	p, err := tree.Parse(b)
	if err != nil {
		warn(fault.AsRecovered(err, b.Offset))
		return nil
	}
	h := p.(*isom.Stss)
	set := map[int]bool{}
	for _, n := range h.SampleNumber {
		if n == 0 || int(n) > 1<<24 {
			continue
		}
		set[int(n)-1] = true
	}
	tr.SyncTablePresent = true
	return set
}

func validateAgainstMdat(m *Movie, tr *Track, warn func(*fault.Error)) {
	inMdat := func(off, end int64) bool {
		for _, md := range m.Mdats {
			if off >= md.Start && end <= md.End {
				return true
			}
		}
		return false
	}
	kept := tr.Samples[:0]
	for _, s := range tr.Samples {
		end := s.End()
		if s.Size == 0 || s.Offset < 0 || end < s.Offset {
			tr.DroppedSamples++
			tr.IndexComplete = false
			warn(fault.Errorf("mp4.sample", fault.KindParse, "sample %d has invalid range [%d,%d)", s.Index, s.Offset, end).
				At(s.Offset).WithSample(s.Index).Recovered().Wrapped(fault.ErrBadSample))
			continue
		}
		if !inMdat(s.Offset, end) {
			tr.DroppedSamples++
			tr.IndexComplete = false
			warn(fault.Errorf("mp4.sample", fault.KindParse, "sample %d range [%d,%d) is outside every mdat", s.Index, s.Offset, end).
				At(s.Offset).WithSample(s.Index).Recovered().Wrapped(fault.ErrBadSample))
			continue
		}
		s.InMdat = true
		kept = append(kept, s)
	}
	tr.Samples = kept
}

func elstVersion(tree *Tree, b *Box) uint8 {
	if b.PayloadSize < 1 {
		return 0
	}
	one, err := tree.ReadAt(b.PayloadOff, 1)
	if err != nil {
		return 0
	}
	return one[0]
}

func be16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }
func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// String implements fmt.Stringer.
func (t *Track) String() string {
	return fmt.Sprintf("track %d %s %s samples=%d", t.ID, t.Handler, t.Entry, len(t.Samples))
}
