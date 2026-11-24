package pipeline

import (
	"fmt"
	"os"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/parse"
	"mediarun/internal/repair"
)

// processRep handles one DASH representation: validate the init segment
// timescale, then parse and repair every media segment in order.
func processRep(inDir string, rep RepTimeline) (*TrackResult, error) {
	initPath, err := safeJoin(inDir, rep.Init)
	if err != nil {
		return nil, err
	}
	initData, err := os.ReadFile(initPath)
	if err != nil {
		return nil, fault.New(fault.StageInput, "missing initialization segment").
			At(initPath, -1).For(rep.ID, -1).Wrap(err)
	}
	tracks, err := parse.ParseInit(rep.Init, initData)
	if err != nil {
		return nil, err
	}
	for _, ti := range tracks {
		if int64(ti.Timescale) != rep.Timescale {
			return nil, fault.New(fault.StageInput,
				fmt.Sprintf("init timescale %d != MPD timescale %d", ti.Timescale, rep.Timescale)).
				At(rep.Init, -1).For(rep.ID, -1)
		}
	}
	tr := &TrackResult{ID: rep.ID, Kind: "dash", Bandwidth: rep.Bandwidth, Timescale: rep.Timescale}
	repairer := repair.New(rep.ID, rep.Timescale, false, 1<<31)
	report := &repair.Report{Track: rep.ID}
	var trackEnd, trackStart clock.Ticks
	first := true
	for _, seg := range rep.Segments {
		sr, n, mediaBytes, err := processDASHSegment(inDir, rep, seg, repairer, report)
		if err != nil {
			return nil, err
		}
		tr.SampleCount += n
		if first || sr.ActualStart < trackStart {
			trackStart = sr.ActualStart
		}
		first = false
		if end := sr.ActualStart + sr.Duration; end > trackEnd {
			trackEnd = end
		}
		tr.MediaBytes += mediaBytes
		tr.Segments = append(tr.Segments, *sr)
	}
	tr.Duration = trackEnd - trackStart
	tr.Repairs = report.Entries
	tr.RepairCount = report.Count()
	tr.Stopped = report.Stopped
	return tr, nil
}

// processDASHSegment parses and repairs one DASH segment and maps its sync
// samples onto the master timeline.
func processDASHSegment(inDir string, rep RepTimeline, seg SegTimeline,
	repairer *repair.Repairer, report *repair.Report) (*SegmentResult, int, int64, error) {

	data, digest, err := readSegment(inDir, seg.Media)
	if err != nil {
		return nil, 0, 0, err
	}
	parsed, err := parse.ParseMediaSegment(seg.Media, data)
	if err != nil {
		return nil, 0, 0, err
	}
	sr := &SegmentResult{Path: seg.Media, Digest: digest, DeclaredStart: seg.DeclaredStart}
	sampleCount := 0
	for _, frag := range parsed.Fragments {
		repaired, err := repairer.RepairFragment(frag, report)
		if err != nil {
			return nil, sampleCount, 0, err
		}
		start := clock.MustToMaster(int64(repaired[0]), rep.Timescale)
		last := frag.Samples[len(frag.Samples)-1]
		end := clock.MustToMaster(
			int64(repaired[len(repaired)-1])+int64(last.Duration), rep.Timescale)
		if sr.ActualStart == 0 || start < sr.ActualStart {
			sr.ActualStart = start
		}
		if end > sr.Duration {
			sr.Duration = end
		}
		for i, s := range frag.Samples {
			if !s.Sync {
				continue
			}
			sr.SyncPoints = append(sr.SyncPoints, SyncPoint{
				Tick:        clock.MustToMaster(int64(repaired[i]), rep.Timescale),
				MoofOffset:  frag.MoofOffset,
				SampleIndex: s.Index,
				DataOffset:  s.DataOffset,
			})
		}
		sampleCount += len(frag.Samples)
	}
	sr.Duration -= sr.ActualStart
	sr.AlignmentErrorMs = absMillis(sr.DeclaredStart - sr.ActualStart)
	return sr, sampleCount, parsed.MediaBytes, nil
}
