package container

import (
	"fmt"
	"os"

	"mediacore/internal/core"
	"mediacore/internal/timeline"
)

// Sample is one access unit located in the file, with its validity decided
// rather than assumed. Damage is recorded per sample so gaps are explicit.
type Sample struct {
	Index    int
	Offset   int64
	Size     int64
	Start    timeline.Ticks // source timestamp in track timescale
	Duration timeline.Ticks
	Decoded  bool // payload passed boundary and (when present) CRC checks
	Reason   string
	Err      *core.Error
}

// TrackRecovery is the outcome of applying the index to the file and, when
// needed, rebuilding the index from verifiable sample records.
type TrackRecovery struct {
	TrackID     uint32
	Timescale   timeline.Scale
	Format      AudioFormat
	Samples     []Sample
	Valid       []timeline.Span // contiguous valid runs, no silence insertion
	Gaps        []Gap
	IndexSource string // "tables" or "rescan"
	// SourceFormat is filled from verified records when moov metadata is
	// unavailable; it never overrides declared container metadata.
	SourceFormat AudioFormat
	Warnings     []*core.Error
	TotalBytes   int64
	ValidBytes   int64
}

// Gap is a region the kernel refuses to fabricate audio for.
type Gap struct {
	Start    timeline.Ticks // source ticks
	Duration timeline.Ticks
	Reason   string
	Offset   int64
}

// Recovery reads the smallest amount of media needed to decide which prefix
// is trustworthy.
type Recovery struct {
	File   string
	Scan   *Scan
	Limits Limits
}

// Recover validates every located sample and splits the track into valid runs
// and gaps. It never synthesizes samples: a region that cannot be verified is
// reported as a gap with its byte offset.
func (r Recovery) Recover(tr *TrackScan) (*TrackRecovery, error) {
	// Without moov there is no track metadata at all. Recovery still runs: it
	// rebuilds what it can from verifiable records and reports the rest as
	// gaps. Only a file where no record can be proven is unrecoverable.
	if tr == nil {
		return r.recoverWithoutTrack()
	}
	scale, err := timeline.NewScale(int64(tr.Timescale))
	if err != nil {
		return nil, core.Wrap(core.CodeBadTimestamp, core.StageRecover, false, "invalid track timescale", err).WithTrack(tr.ID, 0)
	}
	out := &TrackRecovery{TrackID: tr.ID, Timescale: scale, Format: tr.Format}

	f, err := os.Open(r.File)
	if err != nil {
		return nil, core.Wrap(core.CodeIO, core.StageRecover, false, "open media", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, core.Wrap(core.CodeIO, core.StageRecover, false, "stat media", err)
	}
	size := st.Size()

	if tr.IndexComplete && len(tr.Sizes) > 0 {
		out.IndexSource = "tables"
		locateFromTables(out, tr, size)
		// One bad sample is a warning; a mostly unreadable table means the
		// tables cannot be trusted and the records must be re-proven instead.
		if invalidRatio(out) > 0.25 {
			repaired := &TrackRecovery{TrackID: tr.ID, Timescale: out.Timescale, Format: tr.Format,
				IndexSource: "rescan"}
			if rerr := rescan(f, size, repaired, r.Limits, r.Scan, tr); rerr == nil &&
				invalidRatio(repaired) < invalidRatio(out) {
				repaired.Warnings = append(repaired.Warnings, out.Warnings...)
				repaired.Warnings = append(repaired.Warnings, core.New(core.CodeTableTruncated,
					core.StageRecover, true, "sample tables distrusted; index rebuilt from verified records"))
				out = repaired
			}
		}
	} else {
		out.IndexSource = "rescan"
		if err := rescan(f, size, out, r.Limits, r.Scan, tr); err != nil {
			out.Warnings = append(out.Warnings, err)
		}
	}
	finalize(out)
	if len(out.Samples) == 0 {
		return out, core.New(core.CodeUnrecoverable, core.StageRecover, false,
			"no verifiable samples").WithTrack(tr.ID, 0)
	}
	if len(out.Valid) == 0 {
		return out, core.New(core.CodeUnrecoverable, core.StageRecover, false,
			"no contiguous valid prefix").WithTrack(tr.ID, 0)
	}
	return out, nil
}

// invalidRatio is the fraction of located samples that failed validation.
func invalidRatio(out *TrackRecovery) float64 {
	if len(out.Samples) == 0 {
		return 1
	}
	bad := 0
	for _, s := range out.Samples {
		if !s.Decoded {
			bad++
		}
	}
	return float64(bad) / float64(len(out.Samples))
}

// recoverWithoutTrack handles moov-less input. Format facts come from the
// first verified record, which is the only trustworthy source left.
func (r Recovery) recoverWithoutTrack() (*TrackRecovery, error) {
	f, err := os.Open(r.File)
	if err != nil {
		return nil, core.Wrap(core.CodeIO, core.StageRecover, false, "open media", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, core.Wrap(core.CodeIO, core.StageRecover, false, "stat media", err)
	}
	out := &TrackRecovery{IndexSource: "rescan"}
	if rerr := rescan(f, st.Size(), out, r.Limits, r.Scan, nil); rerr != nil {
		return out, rerr
	}
	finalize(out)
	if len(out.Samples) == 0 || len(out.Valid) == 0 {
		return out, core.New(core.CodeUnrecoverable, core.StageRecover, false,
			"no verifiable records after moov loss")
	}
	out.Warnings = append(out.Warnings, core.New(core.CodeMoovMissing, core.StageRecover, true,
		"index rebuilt from verifiable records after moov loss"))
	return out, nil
}

// locateFromTables walks stts/stsc/stco/stsz in lockstep. Samples whose byte
// range or chunk index falls outside the file are marked invalid rather than
// silently truncated.
func locateFromTables(out *TrackRecovery, tr *TrackScan, fileSize int64) {
	sizes := tr.Sizes
	if tr.FixedSize != 0 {
		sizes = make([]uint32, tr.Declared)
		for i := range sizes {
			sizes[i] = tr.FixedSize
		}
	}
	nSamples := len(sizes)
	if tr.Declared < nSamples {
		nSamples = tr.Declared
	}
	if len(tr.Stts) == 0 || len(tr.Stsc) == 0 || len(tr.Offsets) == 0 {
		return
	}

	// Expand chunk -> sample counts.
	chunkSamples := make([]uint32, len(tr.Offsets))
	for i, run := range tr.Stsc {
		first := int(run.FirstChunk) - 1
		end := len(tr.Offsets)
		if i+1 < len(tr.Stsc) {
			end = int(tr.Stsc[i+1].FirstChunk) - 1
		}
		for c := first; c < end && c < len(chunkSamples); c++ {
			if c >= 0 {
				chunkSamples[c] = run.SamplesPerChunk
			}
		}
	}

	// Expand stts into per-sample durations.
	delta := make([]timeline.Ticks, 0, nSamples)
	for _, run := range tr.Stts {
		for i := uint32(0); i < run.Count && len(delta) < nSamples; i++ {
			delta = append(delta, timeline.Ticks(run.Delta))
		}
	}
	for len(delta) < nSamples && len(delta) > 0 {
		delta = append(delta, delta[len(delta)-1])
	}

	// A declared non-zero media start is preserved as the source origin; it
	// is never normalised away, because receivers need to know where the
	// first sample sits.
	ts := timeline.Ticks(0)
	if tr.EditShift > 0 {
		ts = timeline.Ticks(tr.EditShift)
	}
	si := 0
	out.Samples = make([]Sample, 0, nSamples)
	for c, off := range tr.Offsets {
		per := int(chunkSamples[c])
		if per == 0 {
			continue
		}
		pos := off
		for k := 0; k < per && si < nSamples; k++ {
			d := delta[si]
			s := Sample{Index: si, Offset: pos, Size: int64(sizes[si]), Start: ts, Duration: d}
			switch {
			case pos < 0 || pos > fileSize:
				s.Reason = "sample offset outside file"
				s.Err = core.New(core.CodeSampleRange, core.StageRecover, true, s.Reason).
					WithOffset(pos).WithTrack(tr.ID, si)
			case s.Size <= 0:
				s.Reason = "sample size is zero"
				s.Err = core.New(core.CodeSampleRange, core.StageRecover, true, s.Reason).
					WithOffset(pos).WithTrack(tr.ID, si)
			case pos+s.Size > fileSize:
				s.Reason = "sample runs past end of file"
				s.Err = core.New(core.CodeTableTruncated, core.StageRecover, true, s.Reason).
					WithOffset(pos).WithTrack(tr.ID, si)
			default:
				s.Decoded = true
			}
			if s.Err != nil {
				out.Warnings = append(out.Warnings, s.Err)
			}
			out.Samples = append(out.Samples, s)
			ts += d
			pos += s.Size
			si++
		}
	}
	if si < nSamples {
		out.Warnings = append(out.Warnings, core.New(core.CodeTableTruncated, core.StageRecover, true,
			fmt.Sprintf("tables describe only %d of %d samples", si, nSamples)).WithTrack(tr.ID, si))
	}
	out.TotalBytes = fileSize
}

// finalize converts per-sample validity into contiguous spans and merged
// gaps. Contiguous invalid samples share one gap so downstream stages see a
// single missing interval with the reason and offset that started it.
func finalize(out *TrackRecovery) {
	var runStart *Sample
	var prev *Sample
	var gap *Gap
	flushRun := func() {
		if runStart != nil && prev != nil {
			start := runStart.Start
			end := prev.Start + prev.Duration
			out.Valid = append(out.Valid, timeline.Span{Start: start, Len: end - start})
		}
		runStart, prev = nil, nil
	}
	flushGap := func() {
		if gap != nil {
			out.Gaps = append(out.Gaps, *gap)
		}
		gap = nil
	}
	for i := range out.Samples {
		s := &out.Samples[i]
		if s.Decoded {
			flushGap()
			if runStart == nil {
				runStart = s
			}
			prev = s
			out.ValidBytes += s.Size
			continue
		}
		flushRun()
		dur := s.Duration
		if dur <= 0 {
			dur = 1
		}
		if gap == nil {
			gap = &Gap{Start: s.Start, Duration: dur, Reason: s.Reason, Offset: s.Offset}
		} else {
			gap.Duration += dur
		}
	}
	flushRun()
	flushGap()
}
