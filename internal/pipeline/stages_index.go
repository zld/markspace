package pipeline

import (
	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/index"
	"mediarun/internal/verify"
)

// indexOutput is the index stage's persisted output.
type indexOutput = index.Index

// processFragments parses, repairs and maps every segment (fragments
// stage). Tracks are processed one at a time and segments are read
// individually to bound memory.
func processFragments(inDir string, tl *Timeline) (*FragmentsResult, error) {
	out := &FragmentsResult{}
	for _, rep := range tl.Reps {
		tr, err := processRep(inDir, rep)
		if err != nil {
			return nil, err
		}
		out.Tracks = append(out.Tracks, *tr)
	}
	for _, pl := range tl.HLS {
		tr, err := processHLS(inDir, pl)
		if err != nil {
			return nil, err
		}
		out.Tracks = append(out.Tracks, *tr)
	}
	return out, nil
}

// buildIndex assembles the random-access index from all tracks (index
// stage).
func buildIndex(fr *FragmentsResult) (*indexOutput, error) {
	var entries []index.Entry
	var end = fr.mediaEnd()
	for _, tr := range fr.Tracks {
		for _, seg := range tr.Segments {
			for _, sp := range seg.SyncPoints {
				entries = append(entries, index.Entry{
					Tick:         sp.Tick,
					Fragment:     seg.Path,
					MoofOffset:   sp.MoofOffset,
					SampleIndex:  sp.SampleIndex,
					DataOffset:   sp.DataOffset,
					SourceDigest: seg.Digest,
				})
			}
		}
	}
	return index.Assemble(entries, end)
}

// mediaEnd returns the greatest exclusive media end across all segments.
func (fr *FragmentsResult) mediaEnd() clock.Ticks {
	var end clock.Ticks
	for _, tr := range fr.Tracks {
		for _, seg := range tr.Segments {
			if e := seg.ActualStart + seg.Duration; e > end {
				end = e
			}
		}
	}
	return end
}

// computeMetrics derives the verification metrics (verify stage).
func computeMetrics(target int64, fr *FragmentsResult, ix *indexOutput) (*verify.Metrics, error) {
	if ix == nil {
		return nil, fault.New(fault.StageVerify, "nil index")
	}
	m := &verify.Metrics{TargetBitrate: target, IndexEntries: len(ix.Entries)}
	var sel *TrackResult
	for i := range fr.Tracks {
		tr := &fr.Tracks[i]
		m.SampleCount += tr.SampleCount
		m.RepairedTimestamps += tr.RepairCount
		if tr.Duration > m.Duration {
			m.Duration = tr.Duration
		}
		for _, seg := range tr.Segments {
			if seg.AlignmentErrorMs > m.MaxAlignmentErrorMs {
				m.MaxAlignmentErrorMs = seg.AlignmentErrorMs
			}
		}
		if tr.Kind != "dash" {
			continue
		}
		if sel == nil || abs64(tr.Bandwidth-target) < abs64(sel.Bandwidth-target) {
			sel = tr
		}
	}
	if sel == nil && len(fr.Tracks) > 0 {
		sel = &fr.Tracks[0]
	}
	if sel == nil {
		return nil, fault.New(fault.StageVerify, "no tracks to verify")
	}
	m.ActualBitrate = verify.Bitrate(sel.MediaBytes, sel.Duration)
	m.BitrateDeviationPct = verify.DeviationPct(m.ActualBitrate, target)
	m.RandomAccessQueries, m.RandomAccessHits = verify.ProbeRandomAccess(ix, 100)
	m.FillSummary()
	return m, nil
}
