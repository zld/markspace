// Package normalize validates channel layout, sample rate and color metadata
// across representations. An unknown field is never treated as a match: it
// produces an explicit unknown-field finding instead.
package normalize

import (
	"fmt"

	"mediacheck/internal/dash"
	"mediacheck/internal/model"
)

// Field names the three comparable metadata dimensions.
type Field string

const (
	FieldChannels   Field = "channel_layout"
	FieldSampleRate Field = "sample_rate"
	FieldColor      Field = "color_metadata"
)

// Conflict binds a metadata mismatch to a representation and a time interval.
type Conflict struct {
	Field          Field       `json:"field"`
	Representation string      `json:"representation"`
	Peer           string      `json:"peer"`
	Range          model.Range `json:"range"`
	Detail         string      `json:"detail"`
	Unknown        bool        `json:"unknown_field"`
}

// Report is the normalization output.
type Report struct {
	Conflicts []Conflict `json:"conflicts"`
	Issues    []model.Issue
}

// reference chooses the baseline representation per media kind. For video the
// first representation defines expected color; for audio the first defines
// expected channel layout and sample rate.
func reference(manifest *dash.Manifest) (video, audio *dash.RepresentationRecord) {
	for _, r := range manifest.Representations {
		switch r.Kind {
		case "video":
			if video == nil {
				video = r
			}
		case "audio":
			if audio == nil {
				audio = r
			}
		}
	}
	return video, audio
}

// Validate compares every representation against its kind reference over the
// segments that actually exist, so each conflict carries a concrete interval.
func Validate(manifest *dash.Manifest) *Report {
	out := &Report{}
	videoRef, audioRef := reference(manifest)
	for _, r := range manifest.Representations {
		if len(r.Segments) == 0 {
			continue
		}
		span := model.Range{
			Start: r.Segments[0].StartTick,
			End:   r.Segments[len(r.Segments)-1].EndTick,
		}
		switch r.Kind {
		case "video":
			if videoRef != nil {
				compareColor(out, videoRef, r, span)
			}
		case "audio":
			if audioRef != nil {
				compareAudio(out, audioRef, r, span)
			}
		}
	}
	return out
}

func compareAudio(out *Report, ref, r *dash.RepresentationRecord, span model.Range) {
	if r.ID == ref.ID {
		return
	}
	if r.Init.Channels == 0 || ref.Init.Channels == 0 {
		addUnknown(out, FieldChannels, r.ID, ref.ID, span, "channel count unset")
	} else if r.Init.Channels != ref.Init.Channels {
		addConflict(out, FieldChannels, r.ID, ref.ID, span,
			fmt.Sprintf("channels %d vs reference %d", r.Init.Channels, ref.Init.Channels))
	}
	if r.Init.SampleRate == 0 || ref.Init.SampleRate == 0 {
		addUnknown(out, FieldSampleRate, r.ID, ref.ID, span, "sample rate unset")
	} else if r.Init.SampleRate != ref.Init.SampleRate {
		addConflict(out, FieldSampleRate, r.ID, ref.ID, span,
			fmt.Sprintf("sample rate %d vs reference %d", r.Init.SampleRate, ref.Init.SampleRate))
	}
}

func compareColor(out *Report, ref, r *dash.RepresentationRecord, span model.Range) {
	if r.ID == ref.ID {
		return
	}
	// A zero primaries value means the color metadata is absent/unknown and
	// must not be silently accepted as matching.
	if r.Init.ColorPrimaries == 0 || ref.Init.ColorPrimaries == 0 {
		addUnknown(out, FieldColor, r.ID, ref.ID, span, "color primaries unknown")
		return
	}
	if r.Init.ColorPrimaries != ref.Init.ColorPrimaries ||
		r.Init.TransferChar != ref.Init.TransferChar ||
		r.Init.MatrixCoeffs != ref.Init.MatrixCoeffs {
		addConflict(out, FieldColor, r.ID, ref.ID, span, fmt.Sprintf(
			"color cp=%d/tc=%d/mc=%d vs reference cp=%d/tc=%d/mc=%d",
			r.Init.ColorPrimaries, r.Init.TransferChar, r.Init.MatrixCoeffs,
			ref.Init.ColorPrimaries, ref.Init.TransferChar, ref.Init.MatrixCoeffs))
	}
}

func addConflict(out *Report, f Field, id, peer string, span model.Range, detail string) {
	out.Conflicts = append(out.Conflicts, Conflict{
		Field: f, Representation: id, Peer: peer, Range: span, Detail: detail,
	})
	out.Issues = append(out.Issues, model.Issue{
		Kind: model.FaultMetaConflict, Representation: id,
		StartTick: span.Start, EndTick: span.End, Detail: string(f) + ": " + detail,
	})
}

func addUnknown(out *Report, f Field, id, peer string, span model.Range, detail string) {
	out.Conflicts = append(out.Conflicts, Conflict{
		Field: f, Representation: id, Peer: peer, Range: span,
		Detail: detail, Unknown: true,
	})
	out.Issues = append(out.Issues, model.Issue{
		Kind: model.FaultMetaConflict, Representation: id,
		StartTick: span.Start, EndTick: span.End, Detail: "unknown " + string(f) + ": " + detail,
	})
}
