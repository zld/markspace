package timeline

// Decision 记录一次冲突裁决的胜出来源，用于交付可追溯证据。
type Decision struct {
	Track  string
	Field  string
	Winner string
	Losers []string
}

// DefaultColorPriority 为默认色彩来源优先级（前者优先）。
var DefaultColorPriority = []string{"mp4_colr", "mxf_track", "mxf_descriptor", "default"}

// ResolveColor 按可配置优先级裁决冲突的色彩描述。
// 规则确定：优先级列表中排名最高的非零声明胜出；
// 与胜者不一致的其余非零声明记入 Decision。
func ResolveColor(trackName string, inputs []ColorInfo, priority []string) (ColorInfo, []Decision) {
	if len(priority) == 0 {
		priority = DefaultColorPriority
	}
	rank := make(map[string]int, len(priority))
	for i, p := range priority {
		rank[p] = i
	}
	best := -1
	var chosen ColorInfo
	for _, in := range inputs {
		if in.Zero() {
			continue
		}
		r, ok := rank[in.Source]
		if !ok {
			r = len(priority) // 未列入优先级的来源排最后
		}
		if best == -1 || r < best {
			best = r
			chosen = in
		}
	}
	if chosen.Zero() {
		chosen = ColorInfo{Primaries: 1, Transfer: 1, Matrix: 1, Source: "default"}
	}
	var decs []Decision
	for _, in := range inputs {
		if in.Zero() || in.Source == chosen.Source {
			continue
		}
		if in.Primaries != chosen.Primaries || in.Transfer != chosen.Transfer ||
			in.Matrix != chosen.Matrix || in.FullRange != chosen.FullRange {
			decs = append(decs, Decision{
				Track:  trackName,
				Field:  "color",
				Winner: chosen.Source,
				Losers: []string{in.Source},
			})
		}
	}
	return chosen, decs
}
