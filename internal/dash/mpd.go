// Package dash parses DASH Media Presentation Descriptors and ISO BMFF
// initialization/media segments onto the unified integer time base.
package dash

import (
	"fmt"

	"mediacheck/internal/clock"
	"mediacheck/internal/fixture"
	"mediacheck/internal/model"
	"mediacheck/internal/safeio"
)

// InitMeta is the metadata extracted from an initialization segment.
type InitMeta struct {
	Timescale      int64
	Channels       int
	SampleRate     int
	ColorPrimaries uint16
	TransferChar   uint16
	MatrixCoeffs   uint16
}

// SegmentRecord is the parsed, timeline-anchored view of one media segment.
type SegmentRecord struct {
	Representation string
	Sequence       uint32
	StartTick      int64
	EndTick        int64
	DurationTick   int64
	// PlannedStartTick/PlannedEndTick are what the manifest announced; the gap
	// between planned and actual drives the alignment error.
	PlannedStartTick int64
	PlannedEndTick   int64
	Bytes            int
	IndexPresent     bool
	SampleCount      int
	KeyframeCount    int
	Late             bool
	Duplicate        bool
	Truncated        bool
	LocatableOnly    bool // sidx missing: only a minimal locatable interval exists
}

// RepresentationRecord is one parsed adaptation-set entry.
type RepresentationRecord struct {
	ID             string
	Kind           string
	Timescale      int64
	TargetBitrate  int64
	ActualBitrate  int64
	DeviationPct   float64 // (actual-target)/target * 100
	Segments       []*SegmentRecord
	Init           InitMeta
	ManifestStarts []int64
	QualityScores  []float64 // per-sample deterministic quality values
}

// Manifest is the parsed MPD result.
type Manifest struct {
	Representations []*RepresentationRecord
	Issues          []model.Issue
}

// ParseMPD builds the manifest from a synthetic, fully in-memory program.
// Timescale zero/overflow and lossless-time violations abort parsing.
func ParseMPD(prog *fixture.Program, lim safeio.Limits) (*Manifest, error) {
	m := &Manifest{}
	all := append(append([]*fixture.Representation{}, prog.Video...), prog.Audio...)
	for _, fr := range all {
		if fr.Timescale <= 0 {
			return nil, model.NewStageError(model.StageParse,
				fmt.Errorf("%w: representation %s", model.ErrZeroTimescale, fr.ID))
		}
		rec, err := parseRepresentation(fr, lim)
		if err != nil {
			return nil, err
		}
		m.Representations = append(m.Representations, rec)
	}
	return m, nil
}

func parseRepresentation(fr *fixture.Representation, lim safeio.Limits) (*RepresentationRecord, error) {
	rec := &RepresentationRecord{
		ID: fr.ID, Kind: fr.Kind, Timescale: fr.Timescale,
		TargetBitrate: fr.TargetBitrate, ManifestStarts: fr.ManifestStarts,
	}
	meta, err := ParseInit(fr.Init)
	if err != nil {
		return nil, model.NewStageError(model.StageParse, err)
	}
	rec.Init = meta
	var totalBytes int
	var totalTicks int64
	seenSeq := map[uint32]bool{}
	for _, seg := range fr.Segments {
		sr, err := ParseSegment(fr, seg, lim)
		if err != nil {
			return nil, model.NewStageError(model.StageParse, err)
		}
		// Guard the timeline conversion: every tfdt must map losslessly.
		start, err := clock.ToMaster(seg.BaseDecode, fr.Timescale)
		if err != nil {
			return nil, model.NewStageError(model.StageParse, err)
		}
		dur, err := clock.ToMaster(seg.Duration, fr.Timescale)
		if err != nil {
			return nil, model.NewStageError(model.StageParse, err)
		}
		plannedDur, err := clock.ToMaster(seg.PlannedDuration, fr.Timescale)
		if err != nil {
			return nil, model.NewStageError(model.StageParse, err)
		}
		sr.StartTick, sr.DurationTick, sr.EndTick = start, dur, start+dur
		if sr.EndTick <= sr.StartTick {
			return nil, model.NewStageError(model.StageParse, model.ErrDurationBounds)
		}
		// Announced anchor and planned end; actual end follows real duration.
		sr.PlannedStartTick = seg.ManifestStartTick
		sr.PlannedEndTick = seg.ManifestStartTick + plannedDur
		for _, smp := range seg.Samples {
			rec.QualityScores = append(rec.QualityScores, smp.Quality)
		}
		if seg.Late {
			sr.Late = true
		}
		if seenSeq[seg.Sequence] || seg.Duplicate {
			sr.Duplicate = true
		}
		seenSeq[seg.Sequence] = true
		rec.Segments = append(rec.Segments, sr)
		totalBytes += sr.Bytes
		totalTicks += dur
	}
	if totalTicks <= 0 {
		return nil, model.NewStageError(model.StageParse, model.ErrDurationBounds)
	}
	if err := safeio.CheckDuration(totalTicks, lim); err != nil {
		return nil, model.NewStageError(model.StageParse, err)
	}
	// Actual bitrate from measured bytes over exact duration.
	rec.ActualBitrate = int64(float64(totalBytes*8) /
		(float64(totalTicks) / float64(clock.MasterTicksPerSecond)))
	if rec.TargetBitrate > 0 {
		rec.DeviationPct = float64(rec.ActualBitrate-rec.TargetBitrate) /
			float64(rec.TargetBitrate) * 100
	}
	return rec, nil
}
