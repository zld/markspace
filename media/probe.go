package media

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	mp4 "github.com/abema/go-mp4"
)

// Limits bounds resource use for a single input; exceeding them fails the
// input explicitly instead of exhausting memory.
type Limits struct {
	MaxInputBytes int64 `json:"max_input_bytes"`
	MaxSamples    int   `json:"max_samples"`
	MaxDurationUS int64 `json:"max_duration_us"`
}

// DefaultLimits are used unless the caller overrides them.
func DefaultLimits() Limits {
	return Limits{
		MaxInputBytes: 1 << 30, // 1 GiB
		MaxSamples:    5_000_000,
		MaxDurationUS: 12 * 3600 * 1_000_000,
	}
}

// Sample is one sample-table entry with its exact absolute start time.
type Sample struct {
	Number  int   `json:"number"`
	Offset  int64 `json:"offset"`
	Size    int64 `json:"size"`
	StartUS int64 `json:"start_us"`
	DurUS   int64 `json:"dur_us"`
	Key     bool  `json:"key"`
}

// Track is the parsed, per-track timing and sample index.
type Track struct {
	ID          uint32 `json:"id"`
	Handler     string `json:"handler"`
	Kind        string `json:"kind"`
	Timescale   uint32 `json:"timescale"`
	Duration    int64  `json:"duration_us"` // container-declared duration in µs
	StartUS     int64  `json:"start_us"`    // first sample start, µs
	EndUS       int64  `json:"end_us"`      // max end over samples, µs
	MediaUS     int64  `json:"media_us"`    // measured span end-start
	FrameTicks  uint32 `json:"frame_ticks"` // common stts delta; 0 when variable
	SampleCount int    `json:"sample_count"`

	Keyframes []int    `json:"keyframes"` // 1-based sample numbers
	Samples   []Sample `json:"-"`
}

// KeyframeTimes returns the exact start time of every keyframe.
func (t *Track) KeyframeTimes() []Time {
	out := make([]Time, len(t.Keyframes))
	for i, n := range t.Keyframes {
		if n >= 1 && n <= len(t.Samples) {
			out[i] = Time(t.Samples[n-1].StartUS * 1_000)
		}
	}
	return out
}

// FrameInterval returns the nominal constant frame interval, if any.
func (t *Track) FrameInterval() (Time, bool) {
	if t.FrameTicks == 0 || t.Timescale == 0 {
		return 0, false
	}
	m, err := FromTicks(int64(t.FrameTicks), t.Timescale)
	if err != nil {
		return 0, false
	}
	return m, true
}

// Container is the result of probing an MP4/MOV file.
type Container struct {
	Path            string   `json:"path"`
	SHA256          string   `json:"sha256"`
	Size            int64    `json:"size"`
	Brand           string   `json:"brand"`
	MovieTimescale  uint32   `json:"movie_timescale"`
	MovieDurationUS int64    `json:"movie_duration_us"`
	Tracks          []*Track `json:"tracks"`
	Issues          []Issue  `json:"issues"`
}

// VideoTrack returns the first video track, or nil.
func (c *Container) VideoTrack() *Track {
	for _, t := range c.Tracks {
		if t.Kind == "video" {
			return t
		}
	}
	return nil
}

// AudioTrack returns the first audio track, or nil.
func (c *Container) AudioTrack() *Track {
	for _, t := range c.Tracks {
		if t.Kind == "audio" {
			return t
		}
	}
	return nil
}

// FileSHA256 hashes the file without loading it fully into memory.
func FileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, WrapError(KindIO, path, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, WrapError(KindIO, path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// hasTruncatedTopLevelBox reports whether any top-level box declares a size
// that extends past the end of the file.
func hasTruncatedTopLevelBox(f *os.File, size int64) (bool, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	for {
		bi, err := mp4.ReadBoxInfo(f)
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, nil // structural error surfaces later, classified there
		}
		if bi.ExtendToEOF {
			return false, nil
		}
		boxEnd := int64(bi.Offset) + int64(bi.Size)
		if boxEnd > size {
			return true, nil
		}
		if _, err := f.Seek(boxEnd, io.SeekStart); err != nil {
			return false, err
		}
	}
}

// Probe parses a container and returns all tracks with exact sample timings.
// Issues that do not prevent indexing are returned on Container.Issues;
// structural failures are returned as classified errors.
func Probe(path string, lim Limits) (*Container, error) {
	sum, size, err := FileSHA256(path)
	if err != nil {
		return nil, err
	}
	if lim.MaxInputBytes > 0 && size > lim.MaxInputBytes {
		return nil, NewError(KindLimit, path, "input is %d bytes, limit is %d", size, lim.MaxInputBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, WrapError(KindIO, path, err)
	}
	defer f.Close()

	// Detect truncation up front: a top-level box that claims to extend past
	// the end of the file is the signature of an incomplete transfer.
	truncated, err := hasTruncatedTopLevelBox(f, size)
	if err != nil {
		return nil, classifyMP4Error(path, err)
	}
	if _, err := mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (any, error) {
		if h.Path[len(h.Path)-1] == mp4.BoxTypeMoov() {
			return h.Expand()
		}
		return nil, nil
	}); err != nil {
		return nil, classifyMP4Error(path, err)
	}

	c := &Container{Path: path, SHA256: sum, Size: size}
	if boxes, err := mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeMvhd()}); err != nil {
		return nil, classifyMP4Error(path, err)
	} else if len(boxes) == 0 {
		// A file whose bytes end inside its own top-level box cannot be
		// parsed to the end; that is truncation, not arbitrary corruption.
		if truncated {
			return nil, NewError(KindTruncatedInput, path, "file ends before moov is complete")
		}
		return nil, NewError(KindCorruptBox, path, "no mvhd box found")
	} else {
		mvhd := boxes[0].Payload.(*mp4.Mvhd)
		if mvhd.Timescale == 0 {
			return nil, NewError(KindZeroDuration, path, "movie timescale is zero")
		}
		c.MovieTimescale = mvhd.Timescale
		c.MovieDurationUS = MustFromTicks(int64(mvhd.GetDuration()), mvhd.Timescale).Micros()
	}
	if boxes, err := mp4.ExtractBoxWithPayload(f, nil, mp4.BoxPath{mp4.BoxTypeFtyp()}); err == nil && len(boxes) > 0 {
		c.Brand = string(boxes[0].Payload.(*mp4.Ftyp).MajorBrand[:])
	}

	traks, err := mp4.ExtractBox(f, nil, mp4.BoxPath{mp4.BoxTypeMoov(), mp4.BoxTypeTrak()})
	if err != nil {
		return nil, classifyMP4Error(path, err)
	}
	if len(traks) == 0 {
		return nil, NewError(KindUnsupported, path, "no tracks present")
	}
	for _, trak := range traks {
		tr, issues, err := probeTrack(f, path, trak, lim)
		if err != nil {
			return nil, err
		}
		c.Issues = append(c.Issues, issues...)
		c.Tracks = append(c.Tracks, tr)
	}
	return c, nil
}

func probeTrack(f *os.File, file string, trak *mp4.BoxInfo, lim Limits) (*Track, []Issue, error) {
	var issues []Issue
	mdhdBoxes, err := mp4.ExtractBoxWithPayload(f, trak, mp4.BoxPath{mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()})
	if err != nil {
		return nil, nil, classifyMP4Error(file, err)
	}
	if len(mdhdBoxes) == 0 {
		return nil, nil, NewError(KindCorruptBox, file, "track has no mdhd")
	}
	mdhd := mdhdBoxes[0].Payload.(*mp4.Mdhd)
	if mdhd.Timescale == 0 {
		return nil, nil, NewError(KindZeroDuration, file, "track timescale is zero")
	}

	var trackID uint32
	if boxes, err := mp4.ExtractBoxWithPayload(f, trak, mp4.BoxPath{mp4.BoxTypeTkhd()}); err == nil && len(boxes) > 0 {
		trackID = boxes[0].Payload.(*mp4.Tkhd).TrackID
	}
	handler := ""
	if boxes, err := mp4.ExtractBoxWithPayload(f, trak, mp4.BoxPath{mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()}); err == nil && len(boxes) > 0 {
		handler = string(boxes[0].Payload.(*mp4.Hdlr).HandlerType[:])
	}

	t := &Track{
		ID: trackID, Handler: handler, Kind: handlerKind(handler),
		Timescale: mdhd.Timescale,
		Duration:  MustFromTicks(int64(mdhd.GetDuration()), mdhd.Timescale).Micros(),
	}
	if mdhd.GetDuration() == 0 {
		issues = append(issues, Issue{Kind: KindZeroDuration, Severity: SeverityWarning,
			Detail: "track declares duration 0"})
	}
	if err := loadSampleTable(f, file, trak, t, lim, &issues); err != nil {
		return nil, nil, err
	}
	return t, issues, nil
}

func handlerKind(h string) string {
	switch h {
	case "vide":
		return "video"
	case "soun":
		return "audio"
	default:
		return "other"
	}
}

func loadSampleTable(f *os.File, file string, trak *mp4.BoxInfo, t *Track, lim Limits, issues *[]Issue) error {
	stblPath := mp4.BoxPath{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl()}

	extract := func(typ mp4.BoxType) (mp4.IBox, error) {
		boxes, err := mp4.ExtractBoxWithPayload(f, trak, append(append(mp4.BoxPath{}, stblPath...), typ))
		if err != nil {
			return nil, classifyMP4Error(file, err)
		}
		if len(boxes) == 0 {
			return nil, nil
		}
		return boxes[0].Payload, nil
	}

	stszBox, err := extract(mp4.BoxTypeStsz())
	if err != nil {
		return err
	}
	if stszBox == nil {
		*issues = append(*issues, Issue{Kind: KindCorruptBox, Severity: SeverityWarning,
			Detail: "track has no stsz sample size table"})
		return nil
	}
	sttsBox, err := extract(mp4.BoxTypeStts())
	if err != nil {
		return err
	}
	if sttsBox == nil {
		return NewError(KindCorruptBox, file, "track has no time-to-sample table")
	}
	stscBox, err := extract(mp4.BoxTypeStsc())
	if err != nil {
		return err
	}
	stcoBox, err := extract(mp4.BoxTypeStco())
	if err != nil {
		return err
	}
	co64Box, err := extract(mp4.BoxTypeCo64())
	if err != nil {
		return err
	}
	var chunkOffsets []uint64
	if stco, ok := stcoBox.(*mp4.Stco); ok && stco != nil {
		for _, o := range stco.ChunkOffset {
			chunkOffsets = append(chunkOffsets, uint64(o))
		}
	} else if co64, ok := co64Box.(*mp4.Co64); ok && co64 != nil {
		chunkOffsets = co64.ChunkOffset
	} else {
		return NewError(KindCorruptBox, file, "track has no stco/co64 chunk offsets")
	}
	// ctts is optional; a malformed one degrades to decode-time starts.
	var ctts *mp4.Ctts
	if box, err := extract(mp4.BoxTypeCtts()); err == nil && box != nil {
		ctts, _ = box.(*mp4.Ctts)
	} else if err != nil {
		*issues = append(*issues, Issue{Kind: KindCorruptBox, Severity: SeverityWarning,
			Detail: "unreadable ctts box; using decode timestamps"})
	}
	var stss *mp4.Stss
	if box, err := extract(mp4.BoxTypeStss()); err == nil && box != nil {
		stss, _ = box.(*mp4.Stss)
	}

	args := sampleArgs{
		stsz: stszBox.(*mp4.Stsz), stts: sttsBox.(*mp4.Stts), ctts: ctts,
		stsc: stscBox, chunkOffsets: chunkOffsets,
	}
	if stss != nil {
		args.syncSamples = stss.SampleNumber
	}
	return buildSamples(t, args, lim, issues)
}

type sampleArgs struct {
	stsz         *mp4.Stsz
	stts         *mp4.Stts
	ctts         *mp4.Ctts
	stsc         mp4.IBox
	chunkOffsets []uint64
	syncSamples  []uint32
}

func buildSamples(t *Track, a sampleArgs, lim Limits, issues *[]Issue) error {
	n := int(a.stsz.SampleCount)
	if n == 0 {
		*issues = append(*issues, Issue{Kind: KindZeroDuration, Severity: SeverityWarning,
			Detail: "track contains no samples"})
		return nil
	}
	if lim.MaxSamples > 0 && n > lim.MaxSamples {
		return NewError(KindLimit, "", "track has %d samples, limit is %d", n, lim.MaxSamples)
	}
	sizes := make([]uint32, n)
	if a.stsz.SampleSize != 0 {
		for i := range sizes {
			sizes[i] = a.stsz.SampleSize
		}
	} else {
		if len(a.stsz.EntrySize) < n {
			return NewError(KindTruncatedInput, "", "sample size table has %d entries, need %d", len(a.stsz.EntrySize), n)
		}
		copy(sizes, a.stsz.EntrySize[:n])
	}

	if frameTicks, covered := sttsStats(a.stts); covered < uint64(n) {
		*issues = append(*issues, Issue{Kind: KindTruncatedInput, Severity: SeverityError,
			Detail: fmt.Sprintf("time table covers %d of %d samples", covered, n)})
	} else if covered > uint64(n) {
		*issues = append(*issues, Issue{Kind: KindCorruptBox, Severity: SeverityWarning,
			Detail: fmt.Sprintf("time table covers %d samples, table has %d", covered, n)})
	} else {
		t.FrameTicks = frameTicks
	}
	stscCheck(a.stsc, len(a.chunkOffsets), issues)

	// Chunk walking assigns byte offsets in decode order. A shortfall leaves
	// the remaining offsets at zero and is reported rather than hidden.
	offsets := make([]int64, n)
	assigned := 0
	for ci := 0; ci < len(a.chunkOffsets) && assigned < n; ci++ {
		spc := samplesPerChunk(a.stsc, uint32(ci+1))
		var inChunk uint64
		for k := uint32(0); k < spc && assigned < n; k++ {
			offsets[assigned] = int64(a.chunkOffsets[ci] + inChunk)
			inChunk += uint64(sizes[assigned])
			assigned++
		}
	}
	if assigned < n {
		*issues = append(*issues, Issue{Kind: KindTruncatedInput, Severity: SeverityError,
			Detail: fmt.Sprintf("chunk tables cover %d of %d samples", assigned, n)})
	}

	// ctts expansion in sample order; duplicate ctts entries are shifted.
	var compTicks []int64
	if a.ctts != nil {
		var sum uint64
		for _, e := range a.ctts.Entries {
			sum += uint64(e.SampleCount)
		}
		if sum > uint64(n) {
			sum = uint64(n)
		}
		compTicks = make([]int64, sum)
		idx := 0
		for ei, e := range a.ctts.Entries {
			off := a.ctts.GetSampleOffset(ei)
			for k := uint32(0); k < e.SampleCount && idx < len(compTicks); k++ {
				compTicks[idx] = off
				idx++
			}
		}
	}

	// Keyframes: the absence of stss means every sample is a sync sample.
	keySet := make(map[int]bool, len(a.syncSamples))
	for _, s := range a.syncSamples {
		if s >= 1 && int(s) <= n {
			keySet[int(s)] = true
		}
	}
	allKey := len(a.syncSamples) == 0

	t.SampleCount = n
	t.Samples = make([]Sample, n)
	t.Keyframes = t.Keyframes[:0]
	t.Keyframes = make([]int, 0, n)

	var prevStart int64 = -1
	var prevTicks uint64
	prevDelta := int64(0)
	warnNonMono, warnWrap, warnedGap := false, false, 0
	filled := 0
	for i := 0; i < n; i++ {
		startTicks, deltaTicks, err := tickAt(a.stts, i)
		if err != nil {
			if IsKind(err, KindTruncatedInput) {
				*issues = append(*issues, Issue{Kind: KindTimestampGap, Severity: SeverityError,
					Detail: fmt.Sprintf("timing ends after sample %d", i),
					At:     i + 1, TimeUS: prevStart})
				break
			}
			return err
		}
		startUs := MustFromTicks(int64(startTicks), t.Timescale).Micros()
		durUs := MustFromTicks(int64(deltaTicks), t.Timescale).Micros()
		// Presentation time drives the reported timeline; non-monotonic
		// presentation is what a consumer actually observes.
		compUs := int64(0)
		if i < len(compTicks) {
			compUs = MustFromTicks(compTicks[i], t.Timescale).Micros()
		}
		presUs := startUs + compUs

		if prevStart >= 0 && presUs < prevStart {
			// A reset from the top half of the 32-bit tick domain back to the
			// bottom half is a wraparound; other backward steps are
			// non-monotonic timestamps. Both are reported, never reordered.
			if prevTicks >= 1<<31 && startTicks < 1<<31 {
				if !warnWrap {
					warnWrap = true
					*issues = append(*issues, Issue{Kind: KindWraparound, Severity: SeverityError,
						Detail: fmt.Sprintf("32-bit timestamp wraparound at sample %d", i+1),
						At:     i + 1, TimeUS: presUs})
				}
			} else if !warnNonMono {
				warnNonMono = true
				*issues = append(*issues, Issue{Kind: KindNonMonotonic, Severity: SeverityError,
					Detail: fmt.Sprintf("sample %d starts before its predecessor", i+1),
					At:     i + 1, TimeUS: presUs})
			}
		} else if prevStart >= 0 && prevDelta > 0 && int64(deltaTicks) > 2*prevDelta && durUs >= 50_000 && warnedGap < 4 {
			warnedGap++
			*issues = append(*issues, Issue{Kind: KindTimestampGap, Severity: SeverityWarning,
				Detail: fmt.Sprintf("timing gap of %dus at sample %d", durUs, i+1),
				At:     i + 1, TimeUS: startUs})
		}
		prevStart = presUs
		prevTicks = startTicks
		prevDelta = int64(deltaTicks)

		key := allKey || keySet[i+1]
		if key {
			t.Keyframes = append(t.Keyframes, i+1)
		}
		t.Samples[filled] = Sample{
			Number: i + 1, Offset: offsets[i], Size: int64(sizes[i]),
			StartUS: presUs, DurUS: durUs, Key: key,
		}
		filled++
	}
	t.Samples = t.Samples[:filled]
	if len(t.Samples) == 0 {
		return NewError(KindZeroDuration, "", "no samples are timed")
	}

	var end int64
	for _, s := range t.Samples {
		if e := s.StartUS + s.DurUS; e > end {
			end = e
		}
	}
	t.StartUS = t.Samples[0].StartUS
	t.EndUS = end
	t.MediaUS = end - t.StartUS
	if lim.MaxDurationUS > 0 && t.MediaUS > lim.MaxDurationUS {
		return NewError(KindLimit, "", "track duration %dus exceeds limit %dus", t.MediaUS, lim.MaxDurationUS)
	}
	return nil
}

func sttsStats(stts *mp4.Stts) (frameTicks uint32, covered uint64) {
	if len(stts.Entries) == 1 {
		frameTicks = stts.Entries[0].SampleDelta
	}
	for _, e := range stts.Entries {
		covered += uint64(e.SampleCount)
	}
	return frameTicks, covered
}

func stscCheck(stsc mp4.IBox, chunks int, issues *[]Issue) {
	s, ok := stsc.(*mp4.Stsc)
	if !ok || s == nil || len(s.Entries) == 0 {
		*issues = append(*issues, Issue{Kind: KindCorruptBox, Severity: SeverityError,
			Detail: "sample-to-chunk table is missing or empty"})
		return
	}
	if s.Entries[0].FirstChunk != 1 {
		*issues = append(*issues, Issue{Kind: KindCorruptBox, Severity: SeverityError,
			Detail: "sample-to-chunk table does not begin at chunk 1"})
	}
	if int(s.Entries[len(s.Entries)-1].FirstChunk) > chunks {
		*issues = append(*issues, Issue{Kind: KindCorruptBox, Severity: SeverityWarning,
			Detail: "sample-to-chunk table points past the chunk table"})
	}
}

// tickAt returns the absolute start tick and the duration delta of sample i.
// sampleStartTicks is used by wraparound checks only.
func tickAt(stts *mp4.Stts, i int) (uint64, uint32, error) {
	target := uint64(i)
	var accumulated uint64
	for _, e := range stts.Entries {
		c := uint64(e.SampleCount)
		if target < c {
			start := accumulated + target*uint64(e.SampleDelta)
			if start > 1<<62 {
				return 0, 0, NewError(KindLimit, "", "sample tick overflow at sample %d", i+1)
			}
			return start, e.SampleDelta, nil
		}
		accumulated += c * uint64(e.SampleDelta)
		if accumulated > 1<<62 {
			return 0, 0, NewError(KindLimit, "", "sample tick overflow at sample %d", i+1)
		}
		target -= c
	}
	return 0, 0, NewError(KindTruncatedInput, "", "no timing entry for sample %d", i+1)
}

func samplesPerChunk(stsc mp4.IBox, chunk uint32) uint32 {
	s, ok := stsc.(*mp4.Stsc)
	if !ok || s == nil {
		return 0
	}
	var spc uint32
	for _, e := range s.Entries {
		if chunk >= e.FirstChunk {
			spc = e.SamplesPerChunk
		} else {
			break
		}
	}
	return spc
}

func classifyMP4Error(file string, err error) error {
	if err == nil {
		return nil
	}
	var me *Error
	if errors.As(err, &me) {
		return me
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return &Error{Kind: KindTruncatedInput, File: file, Err: err}
	}
	if strings.Contains(err.Error(), "too large box size") || strings.Contains(err.Error(), "unexpected EOF") {
		return &Error{Kind: KindTruncatedInput, File: file, Err: err}
	}
	return &Error{Kind: KindCorruptBox, File: file, Err: err}
}
