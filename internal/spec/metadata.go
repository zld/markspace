// Package spec validates a track's static layout against the delivery target.
// Every mismatch is reported as a field-level diff carrying both the source
// value and the required value, so a later conversion configuration can cite
// exactly why a change is needed.
package spec

import "mediasync/internal/model"

// Field names are stable identifiers used in reports.
const (
	FieldChannels   = "channels"
	FieldSampleRate = "sample_rate_hz"
	FieldColorPrim  = "color_primaries"
	FieldTransfer   = "transfer_characteristics"
	FieldMatrix     = "matrix_coefficients"
	FieldFullRange  = "full_range"
)

// Diff is one source-vs-target metadata difference.
type Diff struct {
	Field  string      `json:"field"`
	Source interface{} `json:"source"`
	Target interface{} `json:"target"`
}

// Report is the validation outcome for one track.
type Report struct {
	TrackID       string     `json:"track_id"`
	Diffs         []Diff     `json:"diffs"`
	ConversionReq bool       `json:"conversion_required"`
	// RejectReason is set when the layout cannot be handled at all.
	RejectReason string     `json:"reject_reason,omitempty"`
	Source       model.TrackMeta `json:"-"`
}

// NeedsConversion reports whether any field-level diff exists.
func (r *Report) NeedsConversion() bool { return len(r.Diffs) > 0 || r.RejectReason != "" }

// Validate compares one track against the target spec. A zero-valued target
// field means "no constraint" and is skipped. An unrecognisable channel layout
// (zero channels on a real media track) rejects the track with a reason rather
// than inventing a layout.
func Validate(meta model.TrackMeta, target model.TargetSpec) *Report {
	r := &Report{TrackID: meta.TrackID, Source: meta}

	if meta.Codec != model.CodecVideo && meta.Channels == 0 {
		r.RejectReason = "audio track declares zero channels: layout unrecognisable"
		return r
	}

	if target.Channels != 0 && meta.Channels != target.Channels {
		r.Diffs = append(r.Diffs, Diff{
			Field: FieldChannels, Source: meta.Channels, Target: target.Channels,
		})
	}
	if target.SampleRate != 0 && meta.SampleRate != target.SampleRate {
		r.Diffs = append(r.Diffs, Diff{
			Field: FieldSampleRate, Source: meta.SampleRate, Target: target.SampleRate,
		})
	}
	if target.Color != nil {
		r.appendColorDiffs(meta, target.Color)
	}

	r.ConversionReq = len(r.Diffs) > 0
	return r
}

func (r *Report) appendColorDiffs(meta model.TrackMeta, want *model.ColorMeta) {
	if meta.Color == nil {
		// No colour description to compare against; record each required
		// field as missing rather than guessing values.
		r.Diffs = append(r.Diffs,
			Diff{Field: FieldColorPrim, Source: nil, Target: want.ColorPrimaries},
			Diff{Field: FieldTransfer, Source: nil, Target: want.TransferCharacteristics},
			Diff{Field: FieldMatrix, Source: nil, Target: want.MatrixCoefficients},
			Diff{Field: FieldFullRange, Source: nil, Target: want.FullRange},
		)
		return
	}
	got := meta.Color
	if got.ColorPrimaries != want.ColorPrimaries {
		r.Diffs = append(r.Diffs, Diff{Field: FieldColorPrim, Source: got.ColorPrimaries, Target: want.ColorPrimaries})
	}
	if got.TransferCharacteristics != want.TransferCharacteristics {
		r.Diffs = append(r.Diffs, Diff{Field: FieldTransfer, Source: got.TransferCharacteristics, Target: want.TransferCharacteristics})
	}
	if got.MatrixCoefficients != want.MatrixCoefficients {
		r.Diffs = append(r.Diffs, Diff{Field: FieldMatrix, Source: got.MatrixCoefficients, Target: want.MatrixCoefficients})
	}
	if got.FullRange != want.FullRange {
		r.Diffs = append(r.Diffs, Diff{Field: FieldFullRange, Source: got.FullRange, Target: want.FullRange})
	}
}
