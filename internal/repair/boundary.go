// Package repair turns raw, possibly discontinuous per-track timelines into a
// single monotonic nanosecond timeline. Every fix carries the raw offset and
// clock it came from; an undecidable boundary stops only its own track.
package repair

import (
	"avpipe/internal/model"
	"avpipe/internal/scan"
)

// TrackResult is the repaired timeline of one track.
type TrackResult struct {
	Track        int                `json:"track"`
	Samples      []model.SampleMeta `json:"samples"` // corrected, until stop/truncation
	Stopped      bool               `json:"stopped"`
	StopAfterSeq int                `json:"stop_after_seq,omitempty"`
	Boundaries   []model.Boundary   `json:"boundaries"`
	Corrections  []model.Correction `json:"corrections"`
	// Defect is the per-track structured error (illegal boundary or truncation).
	Defect *model.Error `json:"defect,omitempty"`
}

// Report aggregates every track's boundaries, corrections and defects.
type Report struct {
	Tracks      []TrackResult      `json:"tracks"`
	Boundaries  []model.Boundary   `json:"boundaries"`
	Corrections []model.Correction `json:"corrections"`
	Repairable  int                `json:"repairable"`
}

// modulusNS is the 32-bit RTP modulus (2^32 ticks) expressed in nanoseconds.
func modulusNS(rate uint32) model.TimeNS {
	return model.TimeNS((int64(1) << 32) * 1_000_000_000 / int64(rate))
}

// Repair re-detects every discontinuity from the raw samples (it does not trust
// the descriptor's declared type) and applies wrap/source-switch corrections.
func Repair(idx *scan.Index) *Report {
	rep := &Report{}
	for tiIdx := range idx.Tracks {
		ti := &idx.Tracks[tiIdx]
		td := findDesc(idx.Desc.Tracks, ti.Track)
		specs := map[int]model.BoundarySpec{}
		if td != nil {
			for _, b := range td.Boundaries {
				specs[b.AfterSeq] = b
			}
		}
		tr := repairTrack(ti, td, specs)
		// Surface a scan-time truncation as a recoverable per-track defect.
		if ti.Truncated && tr.Defect == nil {
			tr.Defect = ti.Defect
		}
		rep.Tracks = append(rep.Tracks, tr)
		rep.Boundaries = append(rep.Boundaries, tr.Boundaries...)
		rep.Corrections = append(rep.Corrections, tr.Corrections...)
		for _, b := range tr.Boundaries {
			if b.Repairable {
				rep.Repairable++
			}
		}
	}
	return rep
}

func repairTrack(ti *scan.TrackIndex, td *model.TrackDesc,
	specs map[int]model.BoundarySpec) TrackResult {

	tr := TrackResult{Track: ti.Track}
	if len(ti.Samples) == 0 {
		return tr
	}
	var shift model.TimeNS

	first := ti.Samples[0]
	first.FixedTimeNS = first.RawTimeNS
	tr.Samples = append(tr.Samples, first)

	for i := 1; i < len(ti.Samples); i++ {
		prev, cur := tr.Samples[len(tr.Samples)-1], ti.Samples[i]
		afterSeq := cur.Seq - 1
		spec, declared := specs[afterSeq]
		if declared && spec.Type == model.BTruncate {
			break // truncated tail has no usable sample pair; scan reported it
		}

		sourceChanged := cur.Clock.SourceID != prev.Clock.SourceID
		wrap := model.IsWrap(prev.RawTS, cur.RawTS)
		// A non-increasing raw value on the same source (including a stall at
		// the same tick) is not a clean wrap and therefore undecidable.
		backward := cur.RawTS <= prev.RawTS

		// Classification comes from the raw stream alone; the descriptor only
		// states where a clip point sits and anchors switch target times.
		var kind model.Kind
		switch {
		case sourceChanged:
			kind = model.KindSwitch
		case wrap:
			kind = model.KindWrap
		case backward:
			kind = model.KindIllegal
		default:
			kind = model.KindContinuous
		}

		if kind == model.KindIllegal {
			tr.Boundaries = append(tr.Boundaries, model.Boundary{
				Track: ti.Track, AfterSeq: afterSeq, Offset: cur.InputOffset,
				Kind: model.KindIllegal, Repairable: false})
			tr.Stopped = true
			tr.StopAfterSeq = prev.Seq
			tr.Defect = model.NewError(model.CodeIllegalTimestamp, false,
				"illegal timestamp boundary at seq %d", cur.Seq).
				AtTrack(ti.Track, cur.InputOffset)
			return tr
		}

		if kind == model.KindWrap {
			applyWrap(&tr, &shift, cur)
		} else if kind == model.KindSwitch {
			applySwitch(&tr, &shift, prev, cur, spec)
		}

		cur.FixedTimeNS = cur.RawTimeNS + shift
		if cur.FixedTimeNS <= prev.FixedTimeNS {
			tr.Stopped = true
			tr.StopAfterSeq = prev.Seq
			tr.Defect = model.NewError(model.CodeIllegalTimestamp, false,
				"non-monotonic repaired time at seq %d", cur.Seq).
				AtTrack(ti.Track, cur.InputOffset)
			return tr
		}

		// Only declared clip/transition points are reported as boundaries.
		if declared {
			repairable := kind == model.KindWrap || kind == model.KindSwitch
			tr.Boundaries = append(tr.Boundaries, model.Boundary{
				Track: ti.Track, AfterSeq: afterSeq, Offset: cur.InputOffset,
				Kind: kind, Repairable: repairable, ExpectedNS: cur.FixedTimeNS})
		}
		tr.Samples = append(tr.Samples, cur)
	}
	return tr
}

func applyWrap(tr *TrackResult, shift *model.TimeNS, cur model.SampleMeta) {
	*shift += modulusNS(cur.Clock.ClockRate)
	fixed := cur.RawTimeNS + *shift
	tr.Corrections = append(tr.Corrections, model.Correction{
		Track: cur.Track, AfterSeq: cur.Seq - 1, Kind: model.KindWrap,
		Offset: cur.InputOffset, RawTimeNS: cur.RawTimeNS, FixedTimeNS: fixed,
		ShiftNS: *shift, Clock: cur.Clock,
	})
}

func applySwitch(tr *TrackResult, shift *model.TimeNS,
	prev, cur model.SampleMeta, spec model.BoundarySpec) {

	expected := spec.ExpectedNS
	if expected == 0 {
		expected = prev.FixedTimeNS
	}
	*shift = expected - cur.RawTimeNS
	tr.Corrections = append(tr.Corrections, model.Correction{
		Track: cur.Track, AfterSeq: cur.Seq - 1, Kind: model.KindSwitch,
		Offset: cur.InputOffset, RawTimeNS: cur.RawTimeNS, FixedTimeNS: expected,
		ShiftNS: *shift, Clock: cur.Clock,
	})
}

func findDesc(tracks []model.TrackDesc, id int) *model.TrackDesc {
	for i := range tracks {
		if tracks[i].ID == id {
			return &tracks[i]
		}
	}
	return nil
}
