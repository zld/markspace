// Package media normalizes track parameters across program sources
// (primary Matroska, backup MP4 init, RTP stream). Inconsistent channel
// layouts, sample rates and colour metadata produce explicit
// normalization decisions; unsupported layouts or conflicting metadata
// are errors located at the offending track.
package media

import (
	"bytes"
	"fmt"

	"github.com/abema/go-mp4"

	"continuity/internal/cerr"
)

// TrackParams describes one media track from one source.
type TrackParams struct {
	Number     uint64 `json:"number"`
	Kind       string `json:"kind"` // "audio" or "video"
	Source     string `json:"source"`
	Channels   int    `json:"channels,omitempty"`
	SampleRate int    `json:"sample_rate,omitempty"`
	ColorSpace string `json:"color_space,omitempty"`
}

// Decision records one explicit normalization choice.
type Decision struct {
	Track  uint64 `json:"track"`
	Aspect string `json:"aspect"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// Colour carries parsed nclx colour metadata.
type Colour struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
}

// ColorName maps a matrix-coefficients value to a color space name.
func ColorName(matrix uint64) (string, bool) {
	switch matrix {
	case 1:
		return "bt709", true
	case 5, 6:
		return "bt601", true
	case 9, 10:
		return "bt2020", true
	}
	return "", false
}

func trackPath(number uint64) string {
	return fmt.Sprintf("track[%d]", number)
}

// Normalize validates and unifies track parameters. Duplicate tracks
// (same number and kind, e.g. primary and backup source) are merged
// into the first occurrence, with conflicts recorded as decisions.
func Normalize(tracks []TrackParams) ([]TrackParams, []Decision, error) {
	var out []TrackParams
	var decisions []Decision
	seen := make(map[[2]interface{}]int)
	for _, t := range tracks {
		if err := validate(t); err != nil {
			return nil, nil, err
		}
		key := [2]interface{}{t.Number, t.Kind}
		if idx, ok := seen[key]; ok {
			base := out[idx]
			if t.Kind == "video" && t.ColorSpace != base.ColorSpace {
				decisions = append(decisions, Decision{
					Track: t.Number, Aspect: "color",
					From: t.ColorSpace, To: base.ColorSpace,
					Reason: fmt.Sprintf("source %s colour metadata differs from %s", t.Source, base.Source),
				})
			}
			if t.Kind == "audio" && t.SampleRate != base.SampleRate {
				decisions = append(decisions, Decision{
					Track: t.Number, Aspect: "sample-rate",
					From: fmt.Sprint(t.SampleRate), To: fmt.Sprint(base.SampleRate),
					Reason: fmt.Sprintf("source %s sample rate differs from %s", t.Source, base.Source),
				})
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, t)
	}
	// Unify audio sample rates and channel layouts across tracks.
	maxRate, maxCh := 0, 0
	for _, t := range out {
		if t.Kind != "audio" {
			continue
		}
		if t.SampleRate > maxRate {
			maxRate = t.SampleRate
		}
		if t.Channels > maxCh {
			maxCh = t.Channels
		}
	}
	for i, t := range out {
		if t.Kind != "audio" {
			continue
		}
		if t.SampleRate != maxRate {
			decisions = append(decisions, Decision{
				Track: t.Number, Aspect: "sample-rate",
				From: fmt.Sprint(t.SampleRate), To: fmt.Sprint(maxRate),
				Reason: "program tracks disagree on sample rate",
			})
			out[i].SampleRate = maxRate
		}
		if t.Channels != maxCh {
			decisions = append(decisions, Decision{
				Track: t.Number, Aspect: "channel-layout",
				From: fmt.Sprint(t.Channels), To: fmt.Sprint(maxCh),
				Reason: "program tracks disagree on channel layout",
			})
			out[i].Channels = maxCh
		}
	}
	return out, decisions, nil
}

func validate(t TrackParams) error {
	path := trackPath(t.Number)
	switch t.Kind {
	case "audio":
		switch t.Channels {
		case 1, 2, 6:
		default:
			return cerr.New(cerr.CodeLayoutUnsupported, -1, path,
				fmt.Sprintf("unsupported channel layout channels=%d", t.Channels))
		}
		if t.SampleRate < 8000 || t.SampleRate > 192000 {
			return cerr.New(cerr.CodeRateUnsupported, -1, path,
				fmt.Sprintf("unsupported sample rate %d", t.SampleRate))
		}
	case "video":
		if t.ColorSpace == "" {
			return cerr.New(cerr.CodeMetadataConflict, -1, path, "missing colour metadata")
		}
		switch t.ColorSpace {
		case "bt601", "bt709", "bt2020":
		default:
			return cerr.New(cerr.CodeMetadataConflict, -1, path,
				fmt.Sprintf("unsupported colour metadata %q", t.ColorSpace))
		}
	default:
		return cerr.New(cerr.CodeTypeUnsupported, -1, path,
			fmt.Sprintf("unsupported track kind %q", t.Kind))
	}
	return nil
}

// ParseMP4Colour extracts nclx colour metadata from an MP4 init
// segment using go-mp4 for container structure parsing.
func ParseMP4Colour(init []byte) (Colour, error) {
	var c Colour
	found := false
	_, err := mp4.ReadBoxStructure(bytes.NewReader(init), func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type != mp4.BoxTypeColr() {
			return nil, nil
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		colr, ok := box.(*mp4.Colr)
		if !ok || colr.ColourType != [4]byte{'n', 'c', 'l', 'x'} {
			return nil, nil
		}
		c = Colour{
			Primaries: colr.ColourPrimaries,
			Transfer:  colr.TransferCharacteristics,
			Matrix:    colr.MatrixCoefficients,
			FullRange: colr.FullRangeFlag,
		}
		found = true
		return nil, nil
	})
	if err != nil {
		return c, cerr.New(cerr.CodeInvalidArgument, -1, "mp4-init", "malformed MP4 init segment")
	}
	if !found {
		return c, cerr.New(cerr.CodeMetadataConflict, -1, "mp4-init", "no nclx colour box found")
	}
	return c, nil
}
