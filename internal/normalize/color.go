package normalize

import (
	"fmt"

	"mediainspect/internal/media"
)

var (
	validPrimaries = map[string]bool{"bt709": true, "bt2020": true, "bt601": true}
	validTransfer  = map[string]bool{"bt709": true, "pq": true, "hlg": true, "srgb": true}
	validMatrix    = map[string]bool{"bt709": true, "bt2020": true, "bt601": true}
	validRange     = map[string]bool{"full": true, "limited": true}
)

// defaultColor is applied when a frame carries no color metadata at all.
var defaultColor = media.ColorMetadata{
	Primaries: "bt709", Transfer: "bt709", Matrix: "bt709", Range: "limited", Present: true,
}

// ColorResult keeps the source values alongside the normalized result and
// every anomaly observed, so damage impact stays traceable.
type ColorResult struct {
	Source     media.ColorMetadata
	Normalized media.ColorMetadata
	Anomalies  []string
}

// Color validates one frame's color metadata. Missing metadata is defaulted
// with an anomaly; illegal values produce per-frame errors and the source
// value is preserved; conflicts are resolved deterministically and recorded.
func Color(in media.ColorMetadata, streamID uint32, seq uint16) (ColorResult, []*media.FrameError) {
	res := ColorResult{Source: in, Normalized: in}
	var errs []*media.FrameError
	mkErr := func(format string, args ...interface{}) {
		errs = append(errs, &media.FrameError{
			Class: media.ClassColor, StreamID: streamID, Seq: seq,
			Detail: fmt.Sprintf(format, args...),
		})
	}

	if !in.Present {
		res.Anomalies = append(res.Anomalies, "color metadata missing; defaulted to bt709/limited")
		res.Normalized = defaultColor
		return res, errs
	}

	check := func(kind, value string, valid map[string]bool) bool {
		if !valid[value] {
			mkErr("illegal %s value %q; source value preserved", kind, value)
			res.Anomalies = append(res.Anomalies,
				fmt.Sprintf("illegal %s %q preserved", kind, value))
			return false
		}
		return true
	}
	check("primaries", in.Primaries, validPrimaries)
	check("transfer", in.Transfer, validTransfer)
	check("matrix", in.Matrix, validMatrix)
	check("range", in.Range, validRange)

	// Deterministic conflict resolution: matrix must agree with primaries.
	if validPrimaries[in.Primaries] && validMatrix[in.Matrix] && in.Matrix != in.Primaries {
		res.Anomalies = append(res.Anomalies, fmt.Sprintf(
			"matrix %q conflicts with primaries %q; normalized to %q", in.Matrix, in.Primaries, in.Primaries))
		res.Normalized.Matrix = in.Primaries
	}
	return res, errs
}
