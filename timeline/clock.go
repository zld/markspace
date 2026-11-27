package timeline

import (
	"errors"
	"math"
)

// ScaleToNS converts a value expressed in unitsPerSecond to int64 nanoseconds.
// It rejects zero timescales, negative values and overflow.
func ScaleToNS(units int64, unitsPerSecond uint64) (int64, error) {
	if unitsPerSecond == 0 {
		return 0, errors.New("zero timescale")
	}
	if units < 0 {
		return 0, errors.New("negative timestamp unit")
	}
	u := uint64(units)
	const maxUnits = math.MaxInt64 / 1_000_000_000
	if u > maxUnits {
		return 0, errors.New("timestamp overflows int64 nanoseconds")
	}
	ns := (u * 1_000_000_000) / unitsPerSecond
	return int64(ns), nil
}

// Anchor pairs a raw clock reading with the reference position it maps to.
// Both are nanoseconds on their respective bases.
type Anchor struct {
	RawNS       int64
	ReferenceNS int64
}

// DriftModel maps raw clock readings onto the reference base:
//
//	reference = (raw - OriginRawNS) * (1 + RatePPM/1e6) + OriginRefNS
//
// RatePPM is the clock rate error in parts per million.
type DriftModel struct {
	OriginRawNS int64   `json:"origin_raw_ns"`
	OriginRefNS int64   `json:"origin_ref_ns"`
	RatePPM     float64 `json:"rate_ppm"`
	MaxAbsResNS int64   `json:"max_abs_residual_ns"`
}

// Apply returns the corrected reference position for a raw reading.
func (d DriftModel) Apply(rawNS int64) (int64, error) {
	delta := rawNS - d.OriginRawNS
	corr := float64(delta) * (1 + d.RatePPM/1e6)
	v := float64(d.OriginRefNS) + corr
	if v < 0 || v > float64(math.MaxInt64) {
		return 0, errors.New("corrected timestamp out of int64 range")
	}
	return int64(math.Round(v)), nil
}

// EstimateDrift fits a DriftModel against at least two anchors by ordinary
// least squares, then reports the maximum absolute residual. With one anchor
// the rate is assumed zero. Anchors must be sorted by RawNS ascending.
func EstimateDrift(anchors []Anchor) (DriftModel, error) {
	if len(anchors) == 0 {
		return DriftModel{}, errors.New("need at least one anchor")
	}
	d := DriftModel{OriginRawNS: anchors[0].RawNS, OriginRefNS: anchors[0].ReferenceNS}
	if len(anchors) == 1 {
		return d, nil
	}
	var sx, sy, sxx, sxy float64
	for _, a := range anchors {
		x := float64(a.RawNS - d.OriginRawNS)
		y := float64(a.ReferenceNS - d.OriginRefNS)
		sx += x
		sy += y
		sxx += x * x
		sxy += x * y
	}
	n := float64(len(anchors))
	denom := n*sxx - sx*sx
	if denom == 0 {
		return d, nil
	}
	slope := (n*sxy - sx*sy) / denom
	d.RatePPM = (slope - 1) * 1e6
	for _, a := range anchors {
		pred, err := d.Apply(a.RawNS)
		if err != nil {
			return DriftModel{}, err
		}
		res := pred - a.ReferenceNS
		if res < 0 {
			res = -res
		}
		if res > d.MaxAbsResNS {
			d.MaxAbsResNS = res
		}
	}
	return d, nil
}
