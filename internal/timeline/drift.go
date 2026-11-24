package timeline

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/zld/mediarc/internal/model"
)

// DriftModel is the stable-anchor affine correction for one track.
type DriftModel struct {
	TrackID      uint32
	BaseSourceNS model.NS
	BaseSharedNS model.NS
	SourceSpanNS model.NS
	SharedSpanNS model.NS
	DriftPPM     int64
}

// CorrectDrift applies stable-anchor affine correction. It never reorders a
// track's confirmed events; it returns an error if integer rounding would.
func CorrectDrift(events []Event, cfg model.Config) ([]Event, []DriftModel, error) {
	byTrack := map[uint32][]model.Anchor{}
	for _, a := range cfg.Anchors {
		if a.Stable {
			byTrack[a.TrackID] = append(byTrack[a.TrackID], a)
		}
	}
	models := map[uint32]*DriftModel{}
	for trackID, anchors := range byTrack {
		m, err := buildDriftModel(trackID, anchors, cfg)
		if err != nil {
			return nil, nil, err
		}
		models[trackID] = m
	}

	out := make([]Event, len(events))
	lastByTrack := map[uint32]model.NS{}
	for i, ev := range events {
		cp := ev
		if m := models[ev.Raw.TrackID]; m != nil {
			cp.TimeNS = m.Apply(ev.SourceNS)
			cp.CorrectionNS = cp.TimeNS - ev.SourceNS
		}
		if last, ok := lastByTrack[ev.Raw.TrackID]; ok && cp.TimeNS < last {
			return nil, nil, &model.AlignmentError{
				Representation: ev.Raw.Representation,
				TrackID:        ev.Raw.TrackID,
				Boundary:       "drift-order",
				DeviationNS:    cp.TimeNS - last,
				ToleranceNS:    0,
			}
		}
		lastByTrack[ev.Raw.TrackID] = cp.TimeNS
		out[i] = cp
	}
	orderedModels := make([]DriftModel, 0, len(models))
	for _, m := range models {
		orderedModels = append(orderedModels, *m)
	}
	sort.Slice(orderedModels, func(i, j int) bool { return orderedModels[i].TrackID < orderedModels[j].TrackID })
	return out, orderedModels, nil
}

func buildDriftModel(trackID uint32, anchors []model.Anchor, cfg model.Config) (*DriftModel, error) {
	sorted := append([]model.Anchor(nil), anchors...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SourceNS < sorted[j].SourceNS })
	if len(sorted) < 2 {
		return nil, &model.ConfigError{Field: "anchors", Value: trackID, Err: fmt.Errorf("need at least two stable anchors")}
	}
	first, last := sorted[0], sorted[len(sorted)-1]
	sourceSpan := last.SourceNS - first.SourceNS
	sharedSpan := last.SharedNS - first.SharedNS
	if sourceSpan <= 0 || sharedSpan <= 0 {
		return nil, &model.ConfigError{Field: "anchors", Value: trackID, Err: fmt.Errorf("anchor times must be strictly increasing")}
	}
	driftPPM := ppmDifference(sharedSpan, sourceSpan)
	if cfg.DriftMaxPPM > 0 && driftPPM > cfg.DriftMaxPPM {
		return nil, &model.ConfigError{
			Field: "drift_max_ppm",
			Value: cfg.DriftMaxPPM,
			Err:   fmt.Errorf("track %d drift %dppm exceeds tolerance", trackID, driftPPM),
		}
	}
	m := &DriftModel{
		TrackID:      trackID,
		BaseSourceNS: first.SourceNS,
		BaseSharedNS: first.SharedNS,
		SourceSpanNS: sourceSpan,
		SharedSpanNS: sharedSpan,
		DriftPPM:     driftPPM,
	}
	for _, a := range sorted {
		predicted := m.Apply(a.SourceNS)
		if dev := (predicted - a.SharedNS).Abs(); dev > cfg.AnchorToleranceNS {
			return nil, &model.AlignmentError{
				TrackID:     trackID,
				Boundary:    "stable-anchor",
				DeviationNS: predicted - a.SharedNS,
				ToleranceNS: cfg.AnchorToleranceNS,
			}
		}
	}
	return m, nil
}

// Apply maps one source timestamp using exact integer rational arithmetic.
func (m DriftModel) Apply(source model.NS) model.NS {
	delta := source - m.BaseSourceNS
	sign := int64(1)
	if delta < 0 {
		sign = -1
		delta = -delta
	}
	num := new(big.Int).Mul(big.NewInt(int64(delta)), big.NewInt(int64(m.SharedSpanNS)))
	den := big.NewInt(int64(m.SourceSpanNS))
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	twiceR := new(big.Int).Lsh(r, 1)
	if twiceR.Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	return m.BaseSharedNS + model.NS(sign*q.Int64())
}

func ppmDifference(a, b model.NS) int64 {
	diff := new(big.Int).Abs(big.NewInt(int64(a - b)))
	num := new(big.Int).Mul(diff, big.NewInt(1_000_000))
	den := big.NewInt(int64(b.Abs()))
	if den.Sign() == 0 {
		return 0
	}
	return num.Quo(num, den).Int64()
}
