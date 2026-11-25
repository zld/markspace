// Package video decides color-metadata handling for delivery.
package video

import (
	"fmt"

	"replay/internal/model"
)

// TargetBT709 is the explicit delivery target for all clips.
var TargetBT709 = model.ColorInfo{Primaries: "bt709", Transfer: "bt709", Matrix: "bt709"}

// Plan is the color decision for one output.
type Plan struct {
	Source  *model.ColorInfo // nil when the input carries no color metadata
	Target  model.ColorInfo
	Convert bool   // an actual conversion is required and will be performed
	Note    string // human-readable rationale, recorded in the manifest
}

// PlanColor decides color handling against the bt709 target.
//   - missing or fully unspecified metadata: reported, passed through,
//     never claimed converted;
//   - matching metadata: pass through;
//   - conflicting metadata: convert to the explicit target and record the
//     source so the conversion is auditable.
func PlanColor(src *model.ColorInfo, rep *model.Report) Plan {
	p := Plan{Source: src, Target: TargetBT709}
	switch {
	case src == nil || unspecified(src):
		p.Note = "input color metadata missing; passing through, no conversion claimed"
		rep.Addf(model.CodeColorMissing, model.SevInfo, "", -1, "%s", p.Note)
	case *src == TargetBT709:
		p.Note = "input already bt709; pass through"
	default:
		p.Convert = true
		p.Note = fmt.Sprintf("converting %s/%s/%s to bt709",
			src.Primaries, src.Transfer, src.Matrix)
		rep.Addf(model.CodeColorConverted, model.SevInfo, "", -1, "%s", p.Note)
	}
	return p
}

// unspecified reports whether every field carries no information.
func unspecified(c *model.ColorInfo) bool {
	blank := func(s string) bool { return s == "" || s == "unspecified" }
	return blank(c.Primaries) && blank(c.Transfer) && blank(c.Matrix)
}
