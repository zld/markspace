package timeline

import (
	"archive/internal/model"
	"archive/internal/parse"
)

// Policy versions every interpretation decision made during compose.
type Policy struct {
	Version       string `json:"version"`
	ColorStrategy string `json:"color_strategy"` // "prefer_bt709" or "first"
}

// DefaultPolicy is the pipeline's current interpretation policy.
func DefaultPolicy() Policy {
	return Policy{Version: "v1", ColorStrategy: "prefer_bt709"}
}

// ColorDecision records the original colour descriptions, the chosen
// one and the policy version that made the choice.
type ColorDecision struct {
	Original      []parse.ColorInfo `json:"original"`
	Chosen        parse.ColorInfo   `json:"chosen"`
	Strategy      string            `json:"strategy"`
	PolicyVersion string            `json:"policy_version"`
	Conflict      bool              `json:"conflict"`
}

// Transform is the per-segment interpretation record: original values
// plus the decisions applied on top of them.
type Transform struct {
	RotationDeg int           `json:"rotation_deg"`
	Matrix      [9]int32      `json:"matrix"`
	Crop        *parse.Crop   `json:"crop,omitempty"`
	Color       ColorDecision `json:"color"`
}

// Segment is one composed output segment built from one source track.
type Segment struct {
	Index     int             `json:"index"`
	Track     int             `json:"track"`
	Source    string          `json:"source"`
	Format    string          `json:"format"`
	Width     uint32          `json:"width"`
	Height    uint32          `json:"height"`
	Interval  model.Interval  `json:"interval"`
	Samples   []AlignedSample `json:"samples"`
	Transform Transform       `json:"transform"`
}

// RotationFromMatrix maps a tkhd display matrix to whole degrees. A
// singular matrix is rejected: export is impossible when the display
// transform cannot be inverted.
func RotationFromMatrix(m [9]int32) (int, error) {
	det := int64(m[0])*int64(m[4]) - int64(m[1])*int64(m[3])
	if det == 0 {
		return 0, errString("display matrix is not invertible")
	}
	const one = 0x10000
	switch {
	case m[0] == one && m[4] == one && m[1] == 0 && m[3] == 0:
		return 0, nil
	case m[1] == one && m[3] == -one && m[0] == 0 && m[4] == 0:
		return 90, nil
	case m[0] == -one && m[4] == -one && m[1] == 0 && m[3] == 0:
		return 180, nil
	case m[1] == -one && m[3] == one && m[0] == 0 && m[4] == 0:
		return 270, nil
	}
	return 0, errString("unsupported (non right-angle) display matrix")
}

type errString string

func (e errString) Error() string { return string(e) }

// ResolveColor applies the colour-conflict policy and records the
// decision with original values and the policy version.
func ResolveColor(colors []parse.ColorInfo, pol Policy) ColorDecision {
	dec := ColorDecision{
		Original:      append([]parse.ColorInfo{}, colors...),
		Strategy:      pol.ColorStrategy,
		PolicyVersion: pol.Version,
	}
	if len(colors) == 0 {
		dec.Chosen = parse.ColorInfo{Primaries: 1, Transfer: 1, Matrix: 1} // default BT.709
		dec.Strategy = "default_bt709"
		return dec
	}
	dec.Chosen = colors[0]
	conflict := false
	for _, c := range colors[1:] {
		if c != colors[0] {
			conflict = true
		}
	}
	dec.Conflict = conflict
	if !conflict {
		return dec
	}
	if pol.ColorStrategy == "prefer_bt709" {
		for _, c := range colors {
			if c.Primaries == 1 && c.Transfer == 1 && c.Matrix == 1 {
				dec.Chosen = c
				return dec
			}
		}
	}
	return dec // "first" strategy, or no BT.709 candidate present
}

// Compose builds the multi-track timeline: rotation, crop and colour
// decisions are applied per segment and recorded for review.
func Compose(tracks []*parse.Track, aligned [][]AlignedSample, pol Policy) ([]Segment, error) {
	if len(tracks) > model.MaxTracks {
		return nil, model.StageFault(model.StageCompose, -1, -1, nil,
			"track count %d exceeds limit %d", len(tracks), model.MaxTracks)
	}
	segs := make([]Segment, 0, len(tracks))
	for i, tr := range tracks {
		if len(aligned[i]) == 0 {
			return nil, model.StageFault(model.StageCompose, i, -1, nil, "track has no aligned samples")
		}
		rot, err := RotationFromMatrix(tr.Matrix)
		if err != nil {
			return nil, model.StageFault(model.StageCompose, i, -1, nil, "%v", err)
		}
		if c := tr.Crop; c != nil {
			if c.W == 0 || c.H == 0 || c.X+c.W > tr.Width || c.Y+c.H > tr.Height {
				return nil, model.StageFault(model.StageCompose, i, -1, nil,
					"crop rectangle %dx%d+%d+%d exceeds frame %dx%d: refusing export",
					c.W, c.H, c.X, c.Y, tr.Width, tr.Height)
			}
		}
		samples := aligned[i]
		end, err := samples[len(samples)-1].Start.Add(samples[len(samples)-1].Duration)
		if err != nil {
			t := samples[len(samples)-1].Start
			return nil, model.StageFault(model.StageCompose, i, -1, &t, "segment end overflow: %v", err)
		}
		dur, err := end.Sub(samples[0].Start)
		if err != nil {
			return nil, model.StageFault(model.StageCompose, i, -1, nil, "segment duration: %v", err)
		}
		segs = append(segs, Segment{
			Index:    i,
			Track:    i,
			Source:   tr.Source,
			Format:   tr.Format,
			Width:    tr.Width,
			Height:   tr.Height,
			Interval: model.Interval{Start: samples[0].Start, Dur: dur},
			Samples:  samples,
			Transform: Transform{
				RotationDeg: rot,
				Matrix:      tr.Matrix,
				Crop:        tr.Crop,
				Color:       ResolveColor(tr.Colors, pol),
			},
		})
	}
	return segs, nil
}
