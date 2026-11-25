package media

import (
	"fmt"

	"archivecheck/internal/diagnostic"
)

// Action states how a metadata mismatch is resolved.
type Action string

const (
	ActionPreserve Action = "preserve"
	ActionRemap    Action = "remap"
	ActionReject   Action = "reject"
)

// ChannelLayout is an explicit layout label. Channel counts alone are not
// enough to infer a layout.
type ChannelLayout string

const (
	LayoutMono   ChannelLayout = "mono"
	LayoutStereo ChannelLayout = "stereo"
	Layout51     ChannelLayout = "5.1"
	Layout71     ChannelLayout = "7.1"
)

// ColorMetadata is the supported subset of MP4 colr/nclx metadata.
type ColorMetadata struct {
	ColourType string `json:"colour_type"`
	Primaries  uint16 `json:"primaries"`
	Transfer   uint16 `json:"transfer"`
	Matrix     uint16 `json:"matrix"`
	FullRange  bool   `json:"full_range"`
}

// String renders a stable, explicit color tuple.
func (c ColorMetadata) String() string {
	return fmt.Sprintf("%s/p%d/t%d/m%d/range=%t", c.ColourType, c.Primaries, c.Transfer, c.Matrix, c.FullRange)
}

// Equal reports exact metadata equality.
func (c ColorMetadata) Equal(o ColorMetadata) bool {
	return c == o
}

// TrackDescription is the accepted code-generated or probed track contract.
type TrackDescription struct {
	Index          int            `json:"index"`
	ID             uint32         `json:"id"`
	Kind           string         `json:"kind"`
	Codec          string         `json:"codec"`
	Timescale      uint32         `json:"timescale"`
	DurationNS     int64          `json:"duration_ns"`
	Width          uint32         `json:"width,omitempty"`
	Height         uint32         `json:"height,omitempty"`
	Channels       uint16         `json:"channels,omitempty"`
	ChannelLayout  ChannelLayout  `json:"channel_layout,omitempty"`
	SampleRate     uint32         `json:"sample_rate,omitempty"`
	Color          *ColorMetadata `json:"color,omitempty"`
	MetadataOffset int64          `json:"metadata_offset"`
}

// RequestedLayout declares the conversion target and the explicit rules that
// permit a remap. Empty/zero fields mean "preserve source".
type RequestedLayout struct {
	ChannelLayout     ChannelLayout
	Channels          uint16
	ChannelMappingID  string
	ResampleAlgorithm string
	SampleRate        uint32
	Color             *ColorMetadata
	ColorConversionID string
}

// Decision is one explainable metadata decision.
type Decision struct {
	Field  string `json:"field"`
	Action Action `json:"action"`
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Reason string `json:"reason"`
}

// LayoutPlan contains all field decisions.
type LayoutPlan struct {
	Accepted  bool       `json:"accepted"`
	Decisions []Decision `json:"decisions"`
}

func knownLayoutChannels(layout ChannelLayout) (uint16, bool) {
	switch layout {
	case LayoutMono:
		return 1, true
	case LayoutStereo:
		return 2, true
	case Layout51:
		return 6, true
	case Layout71:
		return 8, true
	default:
		return 0, false
	}
}

// ValidateDescription checks that explicitly supplied metadata is internally
// consistent and within bounded, supported values.
func ValidateDescription(sample int, d TrackDescription) error {
	if d.Timescale == 0 {
		return diagnostic.New(diagnostic.CodeUnsupportedMetadata, diagnostic.PhaseLayout, sample, d.MetadataOffset,
			"track %d has zero timescale", d.ID)
	}
	if d.DurationNS < 0 {
		return diagnostic.New(diagnostic.CodeNegativeTime, diagnostic.PhaseLayout, sample, d.MetadataOffset,
			"track %d has negative duration %d", d.ID, d.DurationNS)
	}
	if d.ChannelLayout != "" {
		channels, ok := knownLayoutChannels(d.ChannelLayout)
		if !ok {
			return diagnostic.New(diagnostic.CodeUnsupportedMetadata, diagnostic.PhaseLayout, sample, d.MetadataOffset,
				"unsupported channel layout %q", d.ChannelLayout)
		}
		if d.Channels != 0 && d.Channels != channels {
			return diagnostic.New(diagnostic.CodeMetadataConflict, diagnostic.PhaseLayout, sample, d.MetadataOffset,
				"layout %q requires %d channels but description has %d", d.ChannelLayout, channels, d.Channels)
		}
	}
	if d.SampleRate != 0 && (d.SampleRate < 8000 || d.SampleRate > 192000) {
		return diagnostic.New(diagnostic.CodeUnsupportedMetadata, diagnostic.PhaseLayout, sample, d.MetadataOffset,
			"sample rate %d is outside supported archive range 8000..192000", d.SampleRate)
	}
	return nil
}

// AcceptDescription validates a sample-generated track description.
func AcceptDescription(sample int, d TrackDescription) (TrackDescription, error) {
	if err := ValidateDescription(sample, d); err != nil {
		return TrackDescription{}, err
	}
	if d.Channels == 0 && d.ChannelLayout != "" {
		d.Channels, _ = knownLayoutChannels(d.ChannelLayout)
	}
	return d, nil
}

// ResolveLayout never guesses a mapping. Mismatches are preserved, remapped
// only when an explicit rule is supplied, or rejected with a reason.
func ResolveLayout(sample int, source TrackDescription, req RequestedLayout) (LayoutPlan, error) {
	if err := ValidateDescription(sample, source); err != nil {
		return LayoutPlan{}, err
	}
	plan := LayoutPlan{Accepted: true}
	var reject *diagnostic.Error

	channelDecision, err := resolveChannels(source, req)
	plan.Decisions = append(plan.Decisions, channelDecision)
	if err != nil && reject == nil {
		reject = err.(*diagnostic.Error)
	}

	sampleDecision, err := resolveSampleRate(source, req)
	plan.Decisions = append(plan.Decisions, sampleDecision)
	if err != nil && reject == nil {
		reject = err.(*diagnostic.Error)
	}

	colorDecision, err := resolveColor(source, req)
	plan.Decisions = append(plan.Decisions, colorDecision)
	if err != nil && reject == nil {
		reject = err.(*diagnostic.Error)
	}

	if reject != nil {
		plan.Accepted = false
		return plan, reject
	}
	return plan, nil
}

func resolveChannels(source TrackDescription, req RequestedLayout) (Decision, error) {
	from := fmt.Sprintf("%d/%s", source.Channels, source.ChannelLayout)
	targetLayout := req.ChannelLayout
	targetChannels := req.Channels
	if targetLayout == "" && targetChannels == 0 {
		return Decision{Field: "channel_layout", Action: ActionPreserve, From: from, To: from,
			Reason: "no target channel layout requested; preserving source"}, nil
	}
	if targetChannels == 0 && targetLayout != "" {
		targetChannels, _ = knownLayoutChannels(targetLayout)
	}
	to := fmt.Sprintf("%d/%s", targetChannels, targetLayout)
	if source.Channels == targetChannels && (targetLayout == "" || source.ChannelLayout == targetLayout) {
		return Decision{Field: "channel_layout", Action: ActionPreserve, From: from, To: to,
			Reason: "channel count and explicit layout already match"}, nil
	}
	if req.ChannelMappingID == "" {
		err := diagnostic.New(diagnostic.CodeMetadataConflict, diagnostic.PhaseLayout, source.Index, source.MetadataOffset,
			"channel conflict %s -> %s has no declared mapping; refusing to guess", from, to)
		return Decision{Field: "channel_layout", Action: ActionReject, From: from, To: to, Reason: err.Message}, err
	}
	return Decision{Field: "channel_layout", Action: ActionRemap, From: from, To: to,
		Reason: "apply explicitly requested channel mapping " + req.ChannelMappingID}, nil
}

func resolveSampleRate(source TrackDescription, req RequestedLayout) (Decision, error) {
	from := fmt.Sprintf("%dHz", source.SampleRate)
	if req.SampleRate == 0 || req.SampleRate == source.SampleRate {
		return Decision{Field: "sample_rate", Action: ActionPreserve, From: from, To: from,
			Reason: "sample rate matches or no target requested; preserving source"}, nil
	}
	to := fmt.Sprintf("%dHz", req.SampleRate)
	if req.ResampleAlgorithm == "" {
		err := diagnostic.New(diagnostic.CodeMetadataConflict, diagnostic.PhaseLayout, source.Index, source.MetadataOffset,
			"sample-rate conflict %s -> %s has no declared resampler; refusing to guess", from, to)
		return Decision{Field: "sample_rate", Action: ActionReject, From: from, To: to, Reason: err.Message}, err
	}
	return Decision{Field: "sample_rate", Action: ActionRemap, From: from, To: to,
		Reason: "resample using explicitly declared algorithm " + req.ResampleAlgorithm}, nil
}

func resolveColor(source TrackDescription, req RequestedLayout) (Decision, error) {
	from := "absent"
	if source.Color != nil {
		from = source.Color.String()
	}
	if req.Color == nil {
		return Decision{Field: "color_metadata", Action: ActionPreserve, From: from, To: from,
			Reason: "no target color tuple requested; preserving source metadata"}, nil
	}
	to := req.Color.String()
	if source.Color != nil && source.Color.Equal(*req.Color) {
		return Decision{Field: "color_metadata", Action: ActionPreserve, From: from, To: to,
			Reason: "color metadata already matches target"}, nil
	}
	if req.ColorConversionID == "" {
		err := diagnostic.New(diagnostic.CodeMetadataConflict, diagnostic.PhaseLayout, source.Index, source.MetadataOffset,
			"color conflict %s -> %s has no declared conversion rule; refusing to infer primaries", from, to)
		return Decision{Field: "color_metadata", Action: ActionReject, From: from, To: to, Reason: err.Message}, err
	}
	return Decision{Field: "color_metadata", Action: ActionRemap, From: from, To: to,
		Reason: "apply explicitly declared color rule " + req.ColorConversionID}, nil
}
