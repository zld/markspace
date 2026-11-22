// Package stream inspects MP4 sample tables and RTP packet streams, then
// groups structural faults into locatable damage segments.
package stream

import (
	"math"
	"os"
	"sort"

	mp4 "github.com/abema/go-mp4"

	"mediaarchive/internal/clock"
	"mediaarchive/internal/diag"
)

// Sample is one entry from the sample table, expanded with its byte offset and
// DTS on the track's media clock.
type Sample struct {
	Index    int // 1-based, as used by stss
	DTS      int64
	Duration int64
	Offset   int64
	Size     uint32
	Sync     bool
	// Truncated marks a sample whose declared byte extent runs past the file
	// end. Such samples exist only on paper; recovery must not cite them.
	Truncated bool
}

// Track is one trak with an expanded sample table.
type Track struct {
	ID          uint32
	Handler     string
	Timescale   uint32
	Duration    int64
	Samples     []Sample
	SyncSamples []int // 1-based sample numbers, as declared by stss
	DeltasMs    []float64
}

// KeyframeGaps returns exact inter-keyframe durations in milliseconds.
func (t *Track) KeyframeGaps() []float64 {
	var out []float64
	var prev int64 = -1
	for _, s := range t.Samples {
		if !s.Sync {
			continue
		}
		if prev >= 0 {
			out = append(out, float64(s.DTS-prev)*1000/float64(t.Timescale))
		}
		prev = s.DTS
	}
	return out
}

// MaxKeyframeGapMs returns the largest distance between sync samples.
func (t *Track) MaxKeyframeGapMs() float64 {
	m := 0.0
	for _, g := range t.KeyframeGaps() {
		if g > m {
			m = g
		}
	}
	return m
}

// MP4Analysis is the auditable result of scanning one MP4 file.
type MP4Analysis struct {
	Path       string
	Size       int64
	Tracks     []*Track
	Video      *Track
	Faults     []*diag.Fault
	MDATSizes  []int64
	MDATOffset []int64
	MdatBytes  int64
	// DurationMs is the video track duration when present.
	DurationMs float64
	// Truncated reports that declared byte ranges exceed the file.
	Truncated bool
}

// sampleTables accumulates raw stbl tables for one trak.
type sampleTables struct {
	stts     []mp4.SttsEntry
	sizes    []uint32
	stsc     []mp4.StscEntry
	chunks   []int64
	stss     map[uint32]bool
	haveStss bool
}

// AnalyzeMP4 opens path read-only and walks boxes with go-mp4. Only stbl
// metadata is decoded; sample payloads are never read or executed.
func AnalyzeMP4(path string) (*MP4Analysis, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	a := &MP4Analysis{Path: path, Size: st.Size()}
	var cur *Track
	var tab sampleTables
	flush := func() {
		if cur == nil {
			return
		}
		if cur.Timescale != 0 {
			var faults []*diag.Fault
			cur.Samples, faults = expandSamples(cur, tab)
			a.Faults = append(a.Faults, faults...)
			cur.DeltasMs = sampleDeltas(cur)
			a.Tracks = append(a.Tracks, cur)
		}
		cur, tab = nil, sampleTables{}
	}
	handler := func(h *mp4.ReadHandle) (any, error) {
		bi := h.BoxInfo
		switch bi.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl():
			return h.Expand()
		case mp4.BoxTypeTrak():
			flush()
			cur = &Track{}
			return h.Expand()
		case mp4.BoxTypeMdat():
			a.MDATOffset = append(a.MDATOffset, int64(bi.Offset))
			a.MDATSizes = append(a.MDATSizes, int64(bi.Size))
			a.MdatBytes += int64(bi.Size)
			if bi.Offset+bi.Size > uint64(a.Size) {
				a.Truncated = true
				a.Faults = append(a.Faults, diag.New(diag.CodeMP4Truncated, int64(bi.Offset),
					"mdat declares %d bytes but file ends at %d", bi.Size, a.Size))
			}
			return nil, nil
		case mp4.BoxTypeMoof(), mp4.BoxTypeMvex():
			a.Faults = append(a.Faults, diag.New(diag.CodeMP4Structure, int64(bi.Offset),
				"fragmented MP4 (%s) is outside the archive sample-table contract", bi.Type))
			return nil, nil
		}
		if cur == nil {
			return nil, nil
		}
		switch bi.Type {
		case mp4.BoxTypeTkhd(), mp4.BoxTypeMdhd(), mp4.BoxTypeHdlr(),
			mp4.BoxTypeStts(), mp4.BoxTypeStss(), mp4.BoxTypeStsz(),
			mp4.BoxTypeStsc(), mp4.BoxTypeStco(), mp4.BoxTypeCo64():
			box, _, err := h.ReadPayload()
			if err != nil {
				a.Faults = append(a.Faults, diag.New(diag.CodeMP4Structure, int64(bi.Offset),
					"cannot read %s payload: %v", bi.Type, err))
				return nil, nil
			}
			switch b := box.(type) {
			case *mp4.Tkhd:
				cur.ID = b.TrackID
				cur.Duration = int64(b.GetDuration())
			case *mp4.Mdhd:
				cur.Timescale = b.Timescale
				cur.Duration = int64(b.GetDuration())
			case *mp4.Hdlr:
				cur.Handler = string(b.HandlerType[:])
			case *mp4.Stts:
				tab.stts = b.Entries
			case *mp4.Stss:
				tab.haveStss = true
				tab.stss = make(map[uint32]bool, len(b.SampleNumber))
				for _, n := range b.SampleNumber {
					tab.stss[n] = true
					cur.SyncSamples = append(cur.SyncSamples, int(n))
				}
			case *mp4.Stsz:
				if b.SampleSize != 0 {
					tab.sizes = make([]uint32, b.SampleCount)
					for i := range tab.sizes {
						tab.sizes[i] = b.SampleSize
					}
				} else {
					tab.sizes = b.EntrySize
				}
			case *mp4.Stsc:
				tab.stsc = b.Entries
			case *mp4.Stco:
				for _, v := range b.ChunkOffset {
					tab.chunks = append(tab.chunks, int64(v))
				}
			case *mp4.Co64:
				for _, v := range b.ChunkOffset {
					tab.chunks = append(tab.chunks, int64(v))
				}
			}
		}
		return nil, nil
	}
	_, err = mp4.ReadBoxStructure(f, handler)
	if err != nil {
		// The box walk stops at the first structural inconsistency. Record
		// the file extent as the offset so the damage segment covers the tail.
		a.Truncated = true
		f := diag.New(diag.CodeMP4Truncated, a.Size,
			"box walk failed before EOF: %v", err)
		a.Faults = append(a.Faults, f)
	}
	flush()
	finalizeMP4(a)
	return a, nil
}

// expandSamples expands stts/stsz/stsc/stco into concrete samples. Tables are
// validated against each other; mismatches become faults, never panics.
func expandSamples(tr *Track, tab sampleTables) ([]Sample, []*diag.Fault) {
	var faults []*diag.Fault
	if len(tab.stts) == 0 || len(tab.sizes) == 0 {
		faults = append(faults, diag.New(diag.CodeMP4Structure, 0,
			"track %d missing stts or stsz", tr.ID))
		return nil, faults
	}
	samples := make([]Sample, 0, len(tab.sizes))
	var dts int64
	si := 0
	for _, e := range tab.stts {
		if e.SampleDelta == 0 {
			faults = append(faults, diag.New(diag.CodeMP4Structure, int64(si),
				"stts entry with zero sample delta"))
			return samples, faults
		}
		for k := uint32(0); k < e.SampleCount && si < len(tab.sizes); k++ {
			samples = append(samples, Sample{
				Index: si + 1, DTS: dts, Duration: int64(e.SampleDelta), Size: tab.sizes[si],
			})
			dts += int64(e.SampleDelta)
			si++
		}
	}
	if si != len(tab.sizes) {
		faults = append(faults, diag.New(diag.CodeMP4Structure, int64(si),
			"sample count mismatch: stts expands to %d, stsz has %d", si, len(tab.sizes)))
	}
	if len(tab.stsc) > 0 && len(tab.chunks) > 0 {
		sample := 0
		for ei, e := range tab.stsc {
			first := int(e.FirstChunk) - 1
			last := len(tab.chunks)
			if ei+1 < len(tab.stsc) {
				last = int(tab.stsc[ei+1].FirstChunk) - 1
			}
			if first < 0 {
				first = 0
			}
			if first > len(tab.chunks) {
				faults = append(faults, diag.New(diag.CodeMP4Structure, int64(first),
					"stsc references chunk %d beyond stco count %d", first, len(tab.chunks)))
				break
			}
			for c := first; c < last && c < len(tab.chunks) && sample < len(samples); c++ {
				off := tab.chunks[c]
				for k := uint32(0); k < e.SamplesPerChunk && sample < len(samples); k++ {
					samples[sample].Offset = off
					off += int64(samples[sample].Size)
					sample++
				}
			}
		}
	}
	for i := range samples {
		// ISO/IEC 14496-12: a missing stss means every sample is sync.
		samples[i].Sync = true
		if tab.haveStss {
			samples[i].Sync = tab.stss[uint32(samples[i].Index)]
		}
	}
	return samples, faults
}

func sampleDeltas(tr *Track) []float64 {
	if tr.Timescale == 0 || len(tr.Samples) < 2 {
		return nil
	}
	out := make([]float64, 0, len(tr.Samples)-1)
	for i := 1; i < len(tr.Samples); i++ {
		out = append(out, float64(tr.Samples[i].DTS-tr.Samples[i-1].DTS)*1000/float64(tr.Timescale))
	}
	return out
}

// markTruncatedSamples flags samples whose payload is not fully present. A
// truncated last GOP is the normal failure mode of a stopped tape capture.
func markTruncatedSamples(a *MP4Analysis) {
	for _, tr := range a.Tracks {
		for i := range tr.Samples {
			s := &tr.Samples[i]
			if s.Offset+int64(s.Size) > a.Size && s.Size > 0 {
				s.Truncated = true
			}
		}
	}
}

func finalizeMP4(a *MP4Analysis) {
	for _, tr := range a.Tracks {
		if tr.Handler == "vide" && a.Video == nil {
			a.Video = tr
		}
	}
	if a.Video == nil && len(a.Tracks) > 0 {
		a.Video = a.Tracks[0]
	}
	if a.Video != nil && a.Video.Timescale > 0 {
		a.DurationMs = float64(a.Video.Duration) * 1000 / float64(a.Video.Timescale)
		if a.DurationMs == 0 && len(a.Video.Samples) > 0 {
			s := a.Video.Samples[len(a.Video.Samples)-1]
			a.DurationMs = float64(s.DTS+s.Duration) * 1000 / float64(a.Video.Timescale)
		}
	}
	sort.Slice(a.Tracks, func(i, j int) bool { return a.Tracks[i].ID < a.Tracks[j].ID })
}

// Timeline maps a track DTS onto the unified rational time base.
func (t *Track) Timeline(dts int64) clock.Rational {
	if t.Timescale == 0 {
		return clock.Zero
	}
	return clock.FromTicks(dts, int64(t.Timescale))
}

// MedianDeltaMs returns the median observed sample interval in ms.
func (t *Track) MedianDeltaMs() float64 {
	ds := t.DeltasMs
	if len(ds) == 0 {
		return 0
	}
	cp := append([]float64(nil), ds...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

// VFRTolerance is the relative deviation from the median interval above which
// a sample gap is classified as a variable-frame-rate anomaly.
const VFRTolerance = 0.20

// DetectVFR records a fault per interval that deviates from the median by more
// than VFRTolerance. Intervals use real sample time, never an average frame
// rate, so true VFR is not flattened into CFR.
func (t *Track) DetectVFR(srcOffset int64) []*diag.Fault {
	if t.Timescale == 0 || len(t.DeltasMs) < 3 {
		return nil
	}
	med := t.MedianDeltaMs()
	if med <= 0 {
		return nil
	}
	var faults []*diag.Fault
	for i, d := range t.DeltasMs {
		if math.Abs(d-med)/med <= VFRTolerance {
			continue
		}
		s := t.Samples[i+1]
		f := diag.New(diag.CodeVFRGap, srcOffset+s.Offset,
			"sample %d interval %.3f ms deviates from median %.3f ms", s.Index, d, med)
		f.WithMedia(float64(s.DTS) * 1000 / float64(t.Timescale))
		faults = append(faults, f)
	}
	return faults
}

// TimelineRange is the exact media range covered by a track.
func (t *Track) TimelineRange() (clock.Rational, clock.Rational) {
	if len(t.Samples) == 0 || t.Timescale == 0 {
		return clock.Zero, clock.Zero
	}
	first := t.Timeline(t.Samples[0].DTS)
	last := t.Samples[len(t.Samples)-1]
	return first, t.Timeline(last.DTS + last.Duration)
}
