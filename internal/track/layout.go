// Package track normalizes inconsistent legacy capture metadata: channel
// layouts, sample rates and video color fields. When a mapping cannot be
// inferred safely the track is isolated rather than silently rewritten.
package track

import (
	"fmt"

	"mediaarchive/internal/timeline"
)

// Kind distinguishes audio from video tracks.
type Kind int

const (
	KindUnknown Kind = iota
	KindAudio
	KindVideo
)

func (k Kind) String() string {
	switch k {
	case KindAudio:
		return "audio"
	case KindVideo:
		return "video"
	default:
		return "unknown"
	}
}

// ChannelLayout is the normalized speaker layout.
type ChannelLayout string

const (
	LayoutMono       ChannelLayout = "mono"
	LayoutStereo     ChannelLayout = "stereo"
	LayoutSurround51 ChannelLayout = "5.1"
	LayoutIsolated   ChannelLayout = "isolated"
)

// Canonical sample rate for the archive.
const CanonicalSampleRate uint32 = 48000

// ColorFamily groups compatible transfer/primaries/matrix combinations.
type ColorFamily string

const (
	ColorBT709       ColorFamily = "BT.709"
	ColorBT601       ColorFamily = "BT.601"
	ColorUnspecified ColorFamily = "unspecified"
	ColorIsolated    ColorFamily = "isolated"
)

// RawLayout is the untrusted metadata read from a legacy file.
type RawLayout struct {
	TrackID    uint32
	Kind       Kind
	Channels   uint32 // 0 means absent
	SampleRate uint32 // 0 means absent
	// Color fields, zero value means the field is missing.
	ColorPrimaries uint16
	TransferChar   uint16
	MatrixCoeffs   uint16
}

// NormalizedLayout is the explicit, auditable mapping result.
type NormalizedLayout struct {
	TrackID       uint32
	Kind          Kind
	Isolated      bool
	ChannelLayout ChannelLayout
	SampleRate    uint32
	SampleRateMap string // e.g. "passthrough", "44100->48000-rate-tag-only"
	ColorFamily   ColorFamily
	ColorMapping  string // human-readable mapping note
	Actions       []string
	Reasons       []string // populated when isolated
}

// channelMap lists the layouts we can safely assign.
var channelMap = map[uint32]ChannelLayout{
	1: LayoutMono,
	2: LayoutStereo,
	6: LayoutSurround51,
}

// sampleRateMap lists rates with a defined normalization path.
var sampleRateMap = map[uint32]string{
	44100: "44100->48000-rate-tag-only",
	48000: "passthrough",
}

// Matroska color field values (from the Matroska tagging spec).
const (
	cpBT709 uint16 = 1
	cpBT601 uint16 = 5 // SMPTE 170M / BT.601
	tcBT709 uint16 = 1
	tcBT601 uint16 = 6
	mcBT709 uint16 = 1
	mcBT601 uint16 = 6
)

// Normalize maps one raw track description to a canonical layout. It never
// returns an error for an incompatible track: it returns an isolated layout
// with Reasons populated, so the pipeline can quarantine it explicitly.
func Normalize(raw RawLayout) NormalizedLayout {
	out := NormalizedLayout{
		TrackID: raw.TrackID, Kind: raw.Kind,
		ChannelLayout: LayoutIsolated, ColorFamily: ColorIsolated,
	}
	switch raw.Kind {
	case KindAudio:
		normalizeAudio(raw, &out)
	case KindVideo:
		normalizeVideo(raw, &out)
	default:
		isolate(&out, "unknown track kind; cannot infer layout")
	}
	return out
}

func normalizeAudio(raw RawLayout, out *NormalizedLayout) {
	layout, ok := channelMap[raw.Channels]
	if !ok {
		isolate(out, fmt.Sprintf(
			"channel count %d has no safe mapping (supported: 1,2,6)",
			raw.Channels))
		return
	}
	out.ChannelLayout = layout
	out.Actions = append(out.Actions,
		fmt.Sprintf("channels:%d->%s", raw.Channels, layout))

	if raw.SampleRate == 0 {
		isolate(out, "sample rate missing; cannot retag without a source rate")
		return
	}
	mapping, ok := sampleRateMap[raw.SampleRate]
	if !ok {
		isolate(out, fmt.Sprintf(
			"sample rate %d Hz has no normalization path (supported: 44100,48000)",
			raw.SampleRate))
		return
	}
	out.SampleRate = CanonicalSampleRate
	out.SampleRateMap = mapping
	out.Actions = append(out.Actions, "samplerate:"+mapping)
}

func normalizeVideo(raw RawLayout, out *NormalizedLayout) {
	family, note, reasons := mapColor(raw)
	if len(reasons) > 0 {
		isolate(out, reasons...)
		return
	}
	out.ColorFamily = family
	out.ColorMapping = note
	out.Actions = append(out.Actions, "color:"+note)
}

// mapColor resolves the three color fields, distinguishing missing from
// conflicting. A fully missing set defaults to BT.709 with an explicit note;
// a partially present or contradictory set is isolated.
func mapColor(raw RawLayout) (ColorFamily, string, []string) {
	allMissing := raw.ColorPrimaries == 0 && raw.TransferChar == 0 &&
		raw.MatrixCoeffs == 0
	if allMissing {
		return ColorBT709, "missing->default-BT.709", nil
	}
	// Partial presence is ambiguous: we will not guess the other fields.
	if raw.ColorPrimaries == 0 || raw.TransferChar == 0 || raw.MatrixCoeffs == 0 {
		return "", "", []string{
			"partial color metadata; refusing to infer missing fields"}
	}
	switch {
	case raw.ColorPrimaries == cpBT709 && raw.TransferChar == tcBT709 &&
		raw.MatrixCoeffs == mcBT709:
		return ColorBT709, "explicit-BT.709", nil
	case raw.ColorPrimaries == cpBT601 && raw.TransferChar == tcBT601 &&
		raw.MatrixCoeffs == mcBT601:
		return ColorBT601, "explicit-BT.601", nil
	default:
		return "", "", []string{fmt.Sprintf(
			"conflicting color fields primaries=%d transfer=%d matrix=%d",
			raw.ColorPrimaries, raw.TransferChar, raw.MatrixCoeffs)}
	}
}

func isolate(out *NormalizedLayout, reasons ...string) {
	out.Isolated = true
	out.ChannelLayout = LayoutIsolated
	out.ColorFamily = ColorIsolated
	out.Reasons = append(out.Reasons, reasons...)
}

// NormalizeBatch processes a set of tracks and splits survivors from
// isolated tracks, preserving input order for deterministic manifests.
func NormalizeBatch(raw []RawLayout) (survivors []NormalizedLayout, isolated []NormalizedLayout) {
	for _, r := range raw {
		n := Normalize(r)
		if n.Isolated {
			isolated = append(isolated, n)
			continue
		}
		survivors = append(survivors, n)
	}
	return survivors, isolated
}

// IsolationError wraps the reasons for an isolated track using the shared
// error taxonomy, for cross-stage error aggregation.
func IsolationError(n NormalizedLayout) error {
	detail := "no reasons"
	if len(n.Reasons) > 0 {
		detail = fmt.Sprint(n.Reasons)
	}
	return timeline.NewError("track", "normalize", int64(n.TrackID),
		timeline.RecoverNone, detail, timeline.ErrIncompatible)
}
