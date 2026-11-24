package adapter

import "medcheck/internal/clock"

// ColorMetadata is the validated H.264 VUI color description.
type ColorMetadata struct {
	Present   bool   `json:"present"`
	Primaries uint16 `json:"primaries"`
	Transfer  uint16 `json:"transfer"`
	Matrix    uint16 `json:"matrix"`
	FullRange bool   `json:"full_range"`
	Source    string `json:"source"`
}

// ColorPolicy is the explicit fallback used for missing/conflicting metadata.
type ColorPolicy struct {
	Default ColorMetadata
}

// DefaultColorPolicy returns HD BT.709 defaults with a visible source label.
func DefaultColorPolicy() ColorPolicy {
	return ColorPolicy{Default: ColorMetadata{
		Present:   true,
		Primaries: 1, // BT.709
		Transfer:  1,
		Matrix:    1,
		FullRange: false,
		Source:    "policy:default-bt709-v1",
	}}
}

// VideoSpec is the versioned video configuration preserved in MP4 output.
type VideoSpec struct {
	Version string        `json:"version"`
	Codec   string        `json:"codec"`
	Width   int           `json:"width"`
	Height  int           `json:"height"`
	Color   ColorMetadata `json:"color"`
}

// VideoInput contains one synthetic H.264 payload and its VUI metadata.
type VideoInput struct {
	Width    int
	Height   int
	Color    ColorMetadata
	Payload  []byte
	Keyframe bool
}

// VideoOutput is the validated adapter result.
type VideoOutput struct {
	Spec     VideoSpec          `json:"spec"`
	Payload  []byte             `json:"-"`
	Keyframe bool               `json:"keyframe"`
	Errors   []clock.MediaError `json:"errors,omitempty"`
}

// H264V1 validates and preserves H.264/AVC configuration and color metadata.
type H264V1 struct {
	Policy ColorPolicy
}

// NewH264V1 creates the adapter with the explicit default color policy.
func NewH264V1() *H264V1 {
	return &H264V1{Policy: DefaultColorPolicy()}
}

// Version returns the exact registry key.
func (h *H264V1) Version() string { return VersionH264V1 }

// Adapt validates color metadata without silently overwriting its provenance.
func (h *H264V1) Adapt(in VideoInput) (VideoOutput, []clock.MediaError) {
	policy := h.Policy
	if policy.Default.Source == "" {
		policy = DefaultColorPolicy()
	}
	var errs []clock.MediaError
	color := in.Color
	code := clock.CodeColorMetadataPreserved

	switch {
	case !color.Present || color.Primaries == 0 && color.Transfer == 0 && color.Matrix == 0:
		color = policy.Default
		code = clock.CodeColorMetadataDefaulted
		errs = append(errs, clock.MediaError{
			Code: code, Stream: clock.StreamVideo, Source: color.Source,
			Message: "color metadata missing; applied explicit BT.709 policy",
		})
	case colorConflict(color):
		fallback := policy.Default
		fallback.Source = "policy:conflict-default-bt709-v1"
		color = fallback
		code = clock.CodeColorMetadataConflict
		errs = append(errs, clock.MediaError{
			Code: code, Stream: clock.StreamVideo, Source: color.Source,
			Message: "inconsistent VUI color family; applied conflict policy",
		})
	default:
		if color.Source == "" {
			color.Source = "bitstream:vui"
		}
		errs = append(errs, clock.MediaError{
			Code: code, Stream: clock.StreamVideo, Source: color.Source,
			Message: "color metadata preserved",
		})
	}

	out := VideoOutput{
		Spec: VideoSpec{
			Version: VersionH264V1, Codec: "H.264",
			Width: in.Width, Height: in.Height, Color: color,
		},
		Payload: in.Payload, Keyframe: in.Keyframe, Errors: errs,
	}
	return out, errs
}

func colorConflict(c ColorMetadata) bool {
	family := func(v uint16) string {
		switch v {
		case 1, 13:
			return "709"
		case 5, 6:
			return "601"
		case 9, 14, 16, 18:
			return "2020"
		default:
			return "unknown"
		}
	}
	p, t, m := family(c.Primaries), family(c.Transfer), family(c.Matrix)
	// Unknown values are treated as incomplete but not necessarily a conflict;
	// only two clearly different declared families conflict.
	seen := map[string]bool{}
	for _, f := range []string{p, t, m} {
		if f != "unknown" {
			seen[f] = true
		}
	}
	return len(seen) > 1
}
