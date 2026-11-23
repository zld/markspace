package probe

// NormalizedColor is the conflict-resolved color description. Every chosen
// component carries the source box it came from.
type NormalizedColor struct {
	Primaries int64          `json:"primaries"`
	Transfer  int64          `json:"transfer"`
	Matrix    int64          `json:"matrix"`
	Sources   []string       `json:"sources"`
	Conflicts []SourcedValue `json:"conflicts,omitempty"`
}

// Color priority, highest first. ICC profiles win over NCLX numeric triples
// because an embedded profile is the more specific, calibrated declaration;
// among NCLX sources, a present non-zero value beats an unspecified (0) one.
const (
	colorPriorityICC  = 3
	colorPriorityNCLX = 2
	colorPriorityNone = 1
)

// well-known defaults used only when no track declares a value (SMPTE/BT.709).
var defaultColor = NormalizedColor{
	Primaries: 1, Transfer: 1, Matrix: 1,
	Sources: []string{"default/bt709"},
}

// NormalizeColor resolves potentially conflicting color declarations with an
// explicit, deterministic priority and records every contributing source.
func NormalizeColor(ci ColorInfo) NormalizedColor {
	out := NormalizedColor{}
	// ICC profile: highest authority; record it but keep NCLX numbers too.
	if len(ci.ICCProfile) > 0 {
		out.Sources = append(out.Sources, ci.ICCProfile[0].Source)
		out.Conflicts = append(out.Conflicts, ci.ICCProfile...)
	}
	pick := func(vals []SourcedValue, fallback int64) int64 {
		best := fallback
		bestIdx := -1
		for i, v := range vals {
			n, ok := v.Value.(int64)
			if !ok {
				continue
			}
			if n != 0 && bestIdx == -1 {
				best = n
				bestIdx = i
			}
		}
		if bestIdx >= 0 {
			out.Sources = append(out.Sources, vals[bestIdx].Source)
			if len(vals) > 1 {
				out.Conflicts = append(out.Conflicts, vals...)
			}
		}
		return best
	}
	out.Primaries = pick(ci.Primaries, defaultColor.Primaries)
	out.Transfer = pick(ci.Transfer, defaultColor.Transfer)
	out.Matrix = pick(ci.Matrix, defaultColor.Matrix)
	if len(out.Sources) == 0 {
		return defaultColor
	}
	return out
}
