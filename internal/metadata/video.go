package metadata

import (
	"github.com/example/tsrepair/internal/model"
)

// ColorMetadata describes the color descriptor of one video track. A zero
// value in any field means the field is absent from the input.
type ColorMetadata struct {
	PID                uint16
	ColorPrimaries     uint8
	TransferCharacteristics uint8
	MatrixCoefficients uint8
	VideoFullRangeFlag bool
}

// Present reports whether the descriptor declares any color field.
func (c ColorMetadata) Present() bool {
	return c.ColorPrimaries != 0 || c.TransferCharacteristics != 0 ||
		c.MatrixCoefficients != 0
}

// ResolvedColor is the output of conflict normalization. Uncertain fields
// keep the original value and carry the Uncertain flag.
type ResolvedColor struct {
	ColorMetadata
	Uncertain  bool
	Source     string // "input" | "config" | "preserved"
	Conflicts  []ColorConflict
	Missing    []string
}

// ColorConflict records two incompatible values for the same field.
type ColorConflict struct {
	Field    string
	InputVal uint8
	OtherVal uint8
	PID      uint16
	Offset   int64
}

// ColorConfig supplies fallback values used only when the input lacks a
// field. Input-track values always take priority over these.
type ColorConfig struct {
	DefaultPrimaries  uint8
	DefaultTransfer   uint8
	DefaultMatrix     uint8
	DefaultFullRange  bool
}

// ColorNormalizer applies the input-priority rule and logs every missing
// field and every undecidable conflict separately.
type ColorNormalizer struct {
	cfg       ColorConfig
	conflicts []ColorConflict
	missing   int
}

// NewColorNormalizer constructs a normalizer.
func NewColorNormalizer(cfg ColorConfig) *ColorNormalizer {
	return &ColorNormalizer{cfg: cfg}
}

// Normalize merges the input-track descriptor (primary source) with a
// reference descriptor (e.g. container-level metadata). Input values win;
// when both sides declare the same field differently and no rule can decide,
// the original input value is preserved and marked uncertain.
func (n *ColorNormalizer) Normalize(in, ref ColorMetadata, offset int64) ResolvedColor {
	res := ResolvedColor{ColorMetadata: in, Source: "input"}
	mergeField := func(field string, inVal, refVal, defVal uint8, set func(uint8)) {
		switch {
		case inVal != 0:
			if refVal != 0 && refVal != inVal {
				n.conflicts = append(n.conflicts, ColorConflict{
					Field: field, InputVal: inVal, OtherVal: refVal,
					PID: in.PID, Offset: offset})
				// Input priority: keep the input value but flag uncertainty.
				res.Uncertain = true
				res.Conflicts = append(res.Conflicts, ColorConflict{
					Field: field, InputVal: inVal, OtherVal: refVal,
					PID: in.PID, Offset: offset})
			}
			set(inVal)
		case refVal != 0:
			set(refVal)
			res.Source = "config"
		case defVal != 0:
			set(defVal)
			res.Missing = append(res.Missing, field)
			n.missing++
		default:
			res.Missing = append(res.Missing, field)
			n.missing++
		}
	}
	mergeField("color_primaries", in.ColorPrimaries, ref.ColorPrimaries,
		n.cfg.DefaultPrimaries, func(v uint8) { res.ColorPrimaries = v })
	mergeField("transfer", in.TransferCharacteristics, ref.TransferCharacteristics,
		n.cfg.DefaultTransfer, func(v uint8) { res.TransferCharacteristics = v })
	mergeField("matrix", in.MatrixCoefficients, ref.MatrixCoefficients,
		n.cfg.DefaultMatrix, func(v uint8) { res.MatrixCoefficients = v })
	if res.Uncertain {
		res.Source = "preserved"
	}
	return res
}

// UndecidableError converts an unresolved conflict into a classified error.
// Callers decide policy: some runs preserve-and-flag, hard-fail runs call
// this to surface the conflict.
func UndecidableError(pid uint16, offset int64, field string) *model.StreamError {
	return model.New(model.StageMetadata, model.SeverityRecoverable,
		"undecidable color metadata conflict in "+field).
		Wrap(model.ErrColorConflict).At(pid, offset)
}

// Conflicts returns every conflict observed, in order.
func (n *ColorNormalizer) Conflicts() []ColorConflict {
	return append([]ColorConflict(nil), n.conflicts...)
}

// MissingCount returns how many fields had to be filled from config defaults
// or remained absent.
func (n *ColorNormalizer) MissingCount() int { return n.missing }
