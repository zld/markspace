package medpipe

import "fmt"

// Color metadata values use the CICP enumerations (ITU-T H.273 / ISO 23091-2):
// color primaries, transfer characteristics and matrix coefficients. Value 2
// is "unspecified" and is treated as unknown rather than a usable signal.
const (
	CICPUnspecified uint8 = 2
	// A small closed set of common, explicitly supported values.
	PrimariesBT709  uint8 = 1
	PrimariesBT2020 uint8 = 9
	TransferBT709   uint8 = 1
	TransferSRGB    uint8 = 13
	TransferPQ      uint8 = 16
	MatrixBT709     uint8 = 1
	MatrixBT2020NCL uint8 = 9
)

// knownColorValues is the accepted CICP subset. Anything outside it is
// unknown and must not silently override a previously adopted value.
var knownColorValues = map[uint8]struct{}{
	0: {}, 1: {}, 5: {}, 6: {}, 9: {}, 11: {}, 12: {}, 13: {}, 14: {}, 16: {}, 17: {}, 18: {},
}

// ColorTriple is one set of color signaling from a frame or the target.
type ColorTriple struct {
	ColorPrimaries uint8 `json:"color_primaries"`
	Transfer       uint8 `json:"transfer_characteristics"`
	Matrix         uint8 `json:"matrix_coefficients"`
}

// ColorConflict records one disagreement between frames, preserving both the
// source value and the value the pipeline adopted (first-known-wins).
type ColorConflict struct {
	Field      string `json:"field"`
	Offset     int64  `json:"offset"`
	SourceVal  uint8  `json:"source_value"`
	AdoptedVal uint8  `json:"adopted_value"`
}

// ColorUnknown records an unrecognized/unspecified value so it is visible in
// the manifest instead of silently overwriting adopted metadata.
type ColorUnknown struct {
	Field  string `json:"field"`
	Offset int64  `json:"offset"`
	Value  uint8  `json:"value"`
}

// ColorReport is the normalization result for one video track.
type ColorReport struct {
	Adopted   ColorTriple     `json:"adopted"`
	HasSignal bool            `json:"has_signal"`
	Conflicts []ColorConflict `json:"conflicts"`
	Unknown   []ColorUnknown  `json:"unknown_values"`
}

// ColorValidator enforces first-known-wins color metadata across frames.
// Known conflicting values are counted and reported but do not stop the
// pipeline; unknown values never replace an adopted value.
type ColorValidator struct {
	track  string
	b      *ErrorBuilder
	report ColorReport
}

// NewColorValidator starts a validator pinned to one track.
func NewColorValidator(track string) *ColorValidator {
	return &ColorValidator{track: track, b: NewErrorBuilder(track)}
}

// Observe ingests one frame's metadata at its capture byte offset.
func (v *ColorValidator) Observe(meta FrameMeta, offset int64) {
	obs := ColorTriple{meta.ColorPrimaries, meta.Transfer, meta.Matrix}
	fields := []struct {
		name string
		v    uint8
		dst  *uint8
	}{
		{"color_primaries", obs.ColorPrimaries, &v.report.Adopted.ColorPrimaries},
		{"transfer_characteristics", obs.Transfer, &v.report.Adopted.Transfer},
		{"matrix_coefficients", obs.Matrix, &v.report.Adopted.Matrix},
	}
	anyKnown := false
	for _, f := range fields {
		if f.v == 0 {
			continue // field not signaled on this frame
		}
		if _, known := knownColorValues[f.v]; !known || f.v == CICPUnspecified {
			v.report.Unknown = append(v.report.Unknown, ColorUnknown{
				Field: f.name, Offset: offset, Value: f.v,
			})
			continue
		}
		anyKnown = true
		if !v.report.HasSignal {
			*f.dst = f.v // first known value is adopted
			continue
		}
		if *f.dst != f.v {
			v.report.Conflicts = append(v.report.Conflicts, ColorConflict{
				Field: f.name, Offset: offset, SourceVal: f.v, AdoptedVal: *f.dst,
			})
		}
	}
	if anyKnown {
		v.report.HasSignal = true
	}
}

// Finalize returns the report and any errors. Unknown values produce
// recoverable errors (the run continues with the adopted value); a track
// with no usable color signal at all is reported but not failed.
func (v *ColorValidator) Finalize() (ColorReport, []*PipelineError) {
	var errs []*PipelineError
	for _, c := range v.report.Conflicts {
		errs = append(errs, v.b.RecoverableAt(CodeColorConflict,
			fmt.Sprintf("%s: source=%d conflicts with adopted=%d", c.Field, c.SourceVal, c.AdoptedVal),
			"verify the producing encoders; the first-known value is retained", c.Offset))
	}
	for _, u := range v.report.Unknown {
		errs = append(errs, v.b.RecoverableAt(CodeColorUnknown,
			fmt.Sprintf("%s: unknown/unspecified CICP value %d ignored", u.Field, u.Value),
			"tag the stream with a supported CICP value or accept the adopted metadata", u.Offset))
	}
	return v.report, errs
}

// ConflictCount is a convenience accessor for run statistics.
func (r ColorReport) ConflictCount() int { return len(r.Conflicts) }
